package main

import (
 "context"
 "crypto/rand"
 "encoding/csv"
 "encoding/hex"
 "encoding/json"
 "fmt"
 "html/template"
 "log"
 "net/http"
 "os"
 "strconv"
 "strings"
 "time"

 "github.com/jackc/pgx/v5/pgxpool"
)

type Question struct { ID string `json:"id"`; Text string `json:"text"`; Options []string `json:"options"`; Correct int `json:"correct"` }
type Quiz struct { ID string `json:"id"`; Title string `json:"title"`; Description string `json:"description"`; Questions []Question `json:"questions"` }
type App struct { db *pgxpool.Pool; quiz Quiz; page *template.Template }
type Attempt struct { ID int64; Name, Group string; Finished bool; Score int }
type Row struct { Name string `json:"name"`; Score int `json:"score"`; Total int `json:"total"` }
type PageData struct { Quiz Quiz; Attempt Attempt; Ranking []Row; Error string; Total int }

func main() {
 b,err:=os.ReadFile("quizzes/experiment.json"); if err!=nil {log.Fatal(err)}
 var q Quiz; if err=json.Unmarshal(b,&q);err!=nil {log.Fatal(err)}
 if len(q.Questions)==0 {log.Fatal("quiz must have questions")}
 seen:=map[string]bool{}
 for _,v:=range q.Questions {if v.ID==""||seen[v.ID]||len(v.Options)<2||v.Correct<0||v.Correct>=len(v.Options) {log.Fatal("invalid quiz question: ",v.ID)};seen[v.ID]=true}
 url:=os.Getenv("DATABASE_URL");if url=="" {log.Fatal("DATABASE_URL is required")}
 db,err:=pgxpool.New(context.Background(),url);if err!=nil {log.Fatal(err)}
 defer db.Close()
 ctx,cancel:=context.WithTimeout(context.Background(),10*time.Second);defer cancel()
 if err=db.Ping(ctx);err!=nil {log.Fatal(err)}
 schema:=`CREATE TABLE IF NOT EXISTS attempts (
 id BIGSERIAL PRIMARY KEY, token TEXT UNIQUE NOT NULL, name TEXT NOT NULL,
 grp TEXT NOT NULL CHECK(grp IN ('ranking','control')),
 started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 finished_at TIMESTAMPTZ, score INT, stress INT CHECK(stress BETWEEN 1 AND 7),
 baseline_stress INT CHECK(baseline_stress BETWEEN 1 AND 7),
 ranking_seen BOOLEAN NOT NULL DEFAULT FALSE
 );
 CREATE TABLE IF NOT EXISTS answers (
 attempt_id BIGINT NOT NULL REFERENCES attempts(id),
 question_id TEXT NOT NULL, choice INT NOT NULL, correct BOOLEAN NOT NULL,
 elapsed_ms INT NOT NULL DEFAULT 0, PRIMARY KEY(attempt_id,question_id)
 );`
 if _,err=db.Exec(ctx,schema);err!=nil {log.Fatal(err)}
 if _,err=db.Exec(ctx,`ALTER TABLE attempts ADD COLUMN IF NOT EXISTS baseline_stress INT CHECK(baseline_stress BETWEEN 1 AND 7); ALTER TABLE attempts ADD COLUMN IF NOT EXISTS ranking_seen BOOLEAN NOT NULL DEFAULT FALSE`);err!=nil {log.Fatal(err)}
 a:=&App{db:db,quiz:q,page:template.Must(template.New("page").Funcs(template.FuncMap{"add":func(a,b int)int{return a+b},"seq7":func()[]int{return []int{1,2,3,4,5,6,7}}}).Parse(htmlPage))}
 mux:=http.NewServeMux()
 mux.HandleFunc("GET /",a.home)
 mux.HandleFunc("POST /start",a.start)
 mux.HandleFunc("GET /test",a.test)
 mux.HandleFunc("POST /finish",a.finish)
 mux.HandleFunc("GET /result",a.result)
 mux.HandleFunc("GET /api/leaderboard",a.leaderboard)
 mux.HandleFunc("GET /admin/export",a.export)
 mux.HandleFunc("GET /admin",a.admin)
 port:=os.Getenv("PORT");if port=="" {port="8080"}
 log.Println("Listening on :",port)
 log.Fatal(http.ListenAndServe(":"+port,mux))
}
func (a *App) render(w http.ResponseWriter,data PageData) {
 w.Header().Set("Content-Type","text/html; charset=utf-8")
 if err:=a.page.Execute(w,data);err!=nil {log.Println(err)}
}
func (a *App) home(w http.ResponseWriter,r *http.Request) {
 at,_:=a.current(r)
 if at.ID!=0 {if at.Finished {http.Redirect(w,r,"/result",http.StatusSeeOther)} else {http.Redirect(w,r,"/test",http.StatusSeeOther)};return}
 a.render(w,PageData{Quiz:a.quiz})
}
func token() (string,error) {b:=make([]byte,32);if _,err:=rand.Read(b);err!=nil{return "",err};return hex.EncodeToString(b),nil}
func (a *App) start(w http.ResponseWriter,r *http.Request) {
 if at,_:=a.current(r);at.ID!=0 {if at.Finished {http.Redirect(w,r,"/result",303)} else {http.Redirect(w,r,"/test",303)};return}
 if err:=r.ParseForm();err!=nil {http.Error(w,"Bad form",400);return}
 name:=strings.TrimSpace(r.FormValue("name"))
 if len([]rune(name))<2||len([]rune(name))>60||r.FormValue("consent")!="yes" {a.render(w,PageData{Quiz:a.quiz,Error:"Введите имя (2–60 символов) и подтвердите согласие."});return}
 baseline,err:=strconv.Atoi(r.FormValue("baseline_stress"));if err!=nil||baseline<1||baseline>7 {a.render(w,PageData{Quiz:a.quiz,Error:"Оцените напряжение до теста по шкале 1–7."});return}
 t,err:=token();if err!=nil {http.Error(w,"Server error",500);return}
 ctx:=r.Context()
 tx,err:=a.db.Begin(ctx);if err!=nil {http.Error(w,"Database error",500);return}
 defer tx.Rollback(ctx)
 // Serialize assignment to keep the two conditions balanced.
 if _,err=tx.Exec(ctx,"SELECT pg_advisory_xact_lock(894211)");err!=nil {http.Error(w,"Database error",500);return}
 var nr,nc int
 err=tx.QueryRow(ctx,"SELECT count(*) FILTER (WHERE grp='ranking'), count(*) FILTER (WHERE grp='control') FROM attempts").Scan(&nr,&nc)
 if err!=nil {http.Error(w,"Database error",500);return}
 group:="ranking";if nr>nc {group="control"} else if nr==nc {var b [1]byte;if _,err=rand.Read(b[:]);err!=nil {http.Error(w,"Server error",500);return};if b[0]%2==0 {group="control"}}
 _,err=tx.Exec(ctx,"INSERT INTO attempts(token,name,grp,baseline_stress) VALUES($1,$2,$3,$4)",t,name,group,baseline)
 if err!=nil {http.Error(w,"Database error",500);return}
 if err=tx.Commit(ctx);err!=nil {http.Error(w,"Database error",500);return}
 http.SetCookie(w,&http.Cookie{Name:"experiment_session",Value:t,Path:"/",HttpOnly:true,SameSite:http.SameSiteLaxMode,Secure:r.TLS!=nil,MaxAge:86400})
 http.Redirect(w,r,"/test",http.StatusSeeOther)
}
func (a *App) current(r *http.Request) (Attempt,error) {
 c,err:=r.Cookie("experiment_session");if err!=nil {return Attempt{},err}
 var at Attempt
 err=a.db.QueryRow(r.Context(),"SELECT id,name,grp,finished_at IS NOT NULL,COALESCE(score,0) FROM attempts WHERE token=$1",c.Value).Scan(&at.ID,&at.Name,&at.Group,&at.Finished,&at.Score)
 return at,err
}
func (a *App) test(w http.ResponseWriter,r *http.Request) {
 at,err:=a.current(r);if err!=nil {http.Redirect(w,r,"/",303);return}
 if at.Finished {http.Redirect(w,r,"/result",303);return}
 // Copy questions without correct answer indices to avoid exposing answer keys.
 q:=a.quiz;q.Questions=append([]Question(nil),q.Questions...)
 for i:=range q.Questions {q.Questions[i].Correct=-1}
 a.render(w,PageData{Quiz:q,Attempt:at,Total:len(q.Questions)})
}
func (a *App) finish(w http.ResponseWriter,r *http.Request) {
 at,err:=a.current(r);if err!=nil {http.Error(w,"Unauthorized",401);return}
 if at.Finished {http.Redirect(w,r,"/result",303);return}
 if err=r.ParseForm();err!=nil {http.Error(w,"Invalid form",400);return}
 type answer struct {id string;choice,elapsed int;correct bool}
 items:=make([]answer,0,len(a.quiz.Questions));score:=0
 for _,q:=range a.quiz.Questions {
  v:=r.FormValue("answer_"+q.ID);choice,e:=strconv.Atoi(v)
  if e!=nil||choice<0||choice>=len(q.Options) {http.Error(w,"Ответьте на все вопросы",400);return}
  ms,_:=strconv.Atoi(r.FormValue("time_"+q.ID));if ms<0||ms>3600000 {ms=0}
  good:=choice==q.Correct;if good {score++}
  items=append(items,answer{q.ID,choice,ms,good})
 }
 stress,err:=strconv.Atoi(r.FormValue("stress"));if err!=nil||stress<1||stress>7 {http.Error(w,"Оцените напряжение от 1 до 7",400);return}
 ctx:=r.Context();tx,err:=a.db.Begin(ctx);if err!=nil {http.Error(w,"Database error",500);return};defer tx.Rollback(ctx)
 tag,err:=tx.Exec(ctx,"UPDATE attempts SET finished_at=now(),score=$1,stress=$2 WHERE id=$3 AND finished_at IS NULL",score,stress,at.ID)
 if err!=nil {http.Error(w,"Database error",500);return}
 if tag.RowsAffected()==0 {http.Redirect(w,r,"/result",303);return}
 for _,v:=range items {if _,err=tx.Exec(ctx,"INSERT INTO answers(attempt_id,question_id,choice,correct,elapsed_ms) VALUES($1,$2,$3,$4,$5)",at.ID,v.id,v.choice,v.correct,v.elapsed);err!=nil {http.Error(w,"Database error",500);return}}
 if err=tx.Commit(ctx);err!=nil {http.Error(w,"Database error",500);return}
 http.Redirect(w,r,"/result",303)
}
func (a *App) getRanking(ctx context.Context) ([]Row,error) {
 rows,err:=a.db.Query(ctx,"SELECT name,score FROM attempts WHERE grp='ranking' AND finished_at IS NOT NULL ORDER BY score DESC,finished_at ASC LIMIT 50")
 if err!=nil{return nil,err};defer rows.Close()
 out:=[]Row{};for rows.Next(){var v Row;if err=rows.Scan(&v.Name,&v.Score);err!=nil{return nil,err};v.Total=len(a.quiz.Questions);out=append(out,v)}
 return out,rows.Err()
}
func (a *App) leaderboard(w http.ResponseWriter,r *http.Request) {
 at,err:=a.current(r);if err!=nil||at.Group!="ranking" {http.Error(w,"Forbidden",403);return}
 rows,err:=a.getRanking(r.Context());if err!=nil {http.Error(w,"Database error",500);return}
 if !at.Finished {http.Error(w,"Ranking available after completion only",403);return}
 w.Header().Set("Content-Type","application/json");w.Header().Set("Cache-Control","no-store");json.NewEncoder(w).Encode(rows)
}
func (a *App) result(w http.ResponseWriter,r *http.Request) {
 at,err:=a.current(r);if err!=nil {http.Redirect(w,r,"/",303);return}
 if !at.Finished {http.Redirect(w,r,"/test",303);return}
 data:=PageData{Quiz:a.quiz,Attempt:at,Total:len(a.quiz.Questions)}
 if at.Group=="ranking" {data.Ranking,err=a.getRanking(r.Context());if err!=nil {http.Error(w,"Database error",500);return}}
 a.render(w,data)
}

func (a *App) admin(w http.ResponseWriter,r *http.Request) {
 key:=os.Getenv("ADMIN_KEY")
 if key=="" || r.Header.Get("X-Admin-Key")!=key {http.Error(w,"Forbidden: set ADMIN_KEY and send X-Admin-Key header",403);return}
 rows,err:=a.db.Query(r.Context(),`SELECT grp, count(*)::int,
 count(*) FILTER (WHERE finished_at IS NOT NULL)::int,
 COALESCE(avg(score) FILTER (WHERE finished_at IS NOT NULL),0)::float8,
 COALESCE(avg(stress-baseline_stress) FILTER (WHERE finished_at IS NOT NULL),0)::float8,
 COALESCE(avg(extract(epoch from finished_at-started_at)) FILTER (WHERE finished_at IS NOT NULL),0)::float8
 FROM attempts GROUP BY grp ORDER BY grp`)
 if err!=nil {http.Error(w,"Database error",500);return};defer rows.Close()
 w.Header().Set("Content-Type","text/html; charset=utf-8");w.Header().Set("Cache-Control","no-store")
 fmt.Fprint(w,`<!doctype html><html lang="ru"><meta charset="utf-8"><title>Аналитика эксперимента</title><style>body{font:16px system-ui;max-width:950px;margin:50px auto;padding:20px;background:#f4f6fa}table{border-collapse:collapse;background:white;width:100%}td,th{padding:15px;border-bottom:1px solid #ddd;text-align:left}</style><h1>Аналитика эксперимента</h1><p>Средние показатели рассчитаны среди завершивших тест. Данные описательные, без проверки статистической значимости.</p><table><tr><th>Группа</th><th>Начали</th><th>Завершили</th><th>Средний балл</th><th>Изменение напряжения</th><th>Время (сек)</th></tr>`)
 for rows.Next(){var group string;var started,finished int;var score,stress,duration float64
 if err=rows.Scan(&group,&started,&finished,&score,&stress,&duration);err!=nil {log.Println(err);return}
 fmt.Fprintf(w,"<tr><td>%s</td><td>%d</td><td>%d</td><td>%.2f / %d</td><td>%+.2f</td><td>%.1f</td></tr>",template.HTMLEscapeString(group),started,finished,score,len(a.quiz.Questions),stress,duration)
 }
 if err=rows.Err();err!=nil {log.Println(err);return}
 fmt.Fprint(w,"</table><p>Для выгрузки CSV используйте /admin/export с тем же заголовком X-Admin-Key.</p>")
}

func (a *App) export(w http.ResponseWriter,r *http.Request) {
 key:=os.Getenv("ADMIN_KEY")
 if key==""||r.Header.Get("X-Admin-Key")!=key {http.Error(w,"Forbidden",403);return}
 rows,err:=a.db.Query(r.Context(),`SELECT a.id,a.name,a.grp,a.started_at,a.finished_at,a.score,a.stress,a.baseline_stress,a.ranking_seen,
 COALESCE(b.question_id,''),b.choice,b.correct,b.elapsed_ms
 FROM attempts a LEFT JOIN answers b ON b.attempt_id=a.id ORDER BY a.id,b.question_id`)
 if err!=nil {http.Error(w,"Database error",500);return};defer rows.Close()
 w.Header().Set("Content-Type","text/csv; charset=utf-8");w.Header().Set("Content-Disposition","attachment; filename=experiment.csv")
 writer:=csv.NewWriter(w);defer writer.Flush()
 writer.Write([]string{"attempt_id","name","group","started_at","finished_at","score","stress_1_7","baseline_stress_1_7","ranking_seen","question_id","choice_index","correct","elapsed_ms"})
 for rows.Next() {
  var id int64;var name,group,start,qid string;var end *time.Time;var score,stress,baseline,choice,elapsed *int;var correct *bool;var rankingSeen bool
  var started time.Time
  if err=rows.Scan(&id,&name,&group,&started,&end,&score,&stress,&baseline,&rankingSeen,&qid,&choice,&correct,&elapsed);err!=nil {log.Println(err);return}
  start=started.Format(time.RFC3339)
  writer.Write([]string{fmt.Sprint(id),name,group,start,fmtTime(end),fmtPtr(score),fmtPtr(stress),fmtPtr(baseline),strconv.FormatBool(rankingSeen),qid,fmtPtr(choice),fmtBool(correct),fmtPtr(elapsed)})
 }
 if err=rows.Err();err!=nil {log.Println(err)}
}
func fmtPtr(v *int) string {if v==nil{return ""};return strconv.Itoa(*v)}
func fmtBool(v *bool) string {if v==nil{return ""};return strconv.FormatBool(*v)}
func fmtTime(v *time.Time) string {if v==nil{return ""};return v.Format(time.RFC3339)}

const htmlPage = `<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Quiz.Title}}</title><style>
:root{font-family:system-ui,-apple-system,sans-serif;color:#182235;background:#f3f5f9}*{box-sizing:border-box}body{margin:0}.wrap{max-width:1000px;margin:48px auto;padding:0 20px}.card{background:white;border-radius:18px;padding:32px;box-shadow:0 8px 32px #18223512}h1{font-size:32px;margin:0 0 12px}p{line-height:1.55;color:#526075}.grid{display:grid;grid-template-columns:1fr 300px;gap:24px}.question{padding:20px 0;border-bottom:1px solid #e7ebf0}.option{display:block;margin:10px 0;padding:12px;border:1px solid #dfe4ec;border-radius:10px;cursor:pointer}.option:has(input:checked){border-color:#4255df;background:#eef0ff}input[type=text]{padding:12px;width:100%;border:1px solid #cbd2df;border-radius:8px;font-size:16px}button{border:0;background:#4255df;color:white;padding:14px 22px;border-radius:10px;font-size:16px;cursor:pointer;margin-top:16px}.muted{font-size:14px;color:#718096}.rank{position:sticky;top:20px;align-self:start}.rank ol{padding-left:24px}.rank li{padding:6px 0}.error{color:#b42318}.stress{display:flex;gap:8px;flex-wrap:wrap}.stress label{padding:8px;border:1px solid #dfe4ec;border-radius:8px}@media(max-width:740px){.grid{grid-template-columns:1fr}.wrap{margin:20px auto}.card{padding:20px}.rank{position:static}}
</style></head><body><main class="wrap">
{{if eq .Attempt.ID 0}}
<section class="card"><h1>{{.Quiz.Title}}</h1><p>{{.Quiz.Description}}</p><p>В исследовании сравниваются два способа прохождения одинакового теста. Участие добровольное; ваши ответы и оценка напряжения сохраняются для учебного анализа. Псевдоним может быть виден другим участникам в условии с рейтингом. Не вводите полное имя, используйте псевдоним.</p>{{if .Error}}<p class="error">{{.Error}}</p>{{end}}
<form method="post" action="/start"><label>Ваше имя или псевдоним<input name="name" maxlength="60" minlength="2" required autocomplete="nickname"></label><div class="question"><h3>Какое напряжение вы испытываете прямо сейчас, до начала теста?</h3><p class="muted">1 — совсем нет, 7 — очень сильное.</p><div class="stress">{{range seq7}}<label><input type="radio" name="baseline_stress" value="{{.}}" required> {{.}}</label>{{end}}</div></div><p><label><input type="checkbox" name="consent" value="yes" required> Я согласен(на) участвовать и на сохранение ответов для учебного анализа.</label></p><button>Начать тест</button></form></section>
{{else if .Attempt.Finished}}
<section class="card"><h1>Тест завершён</h1><p>Спасибо за участие, {{.Attempt.Name}}!</p><h2>Ваш результат: {{.Attempt.Score}} из {{.Total}}</h2>{{if eq .Attempt.Group "ranking"}}<h3>Рейтинг участников</h3><ol>{{range .Ranking}}<li>{{.Name}} — {{.Score}} / {{.Total}}</li>{{else}}<li>Пока нет результатов</li>{{end}}</ol>{{end}}</section>
{{else}}
<h1>{{.Quiz.Title}}</h1>{{if eq .Attempt.Group "ranking"}}<section class="card"><h2>Соревновательный режим</h2><p><strong>Вы соревнуетесь с другими студентами.</strong> Ваш результат попадёт в общую таблицу лидеров, которую вы увидите после завершения теста.</p></section>{{else}}<section class="card"><p>Пройдите тест. Ваш результат будет показан после завершения.</p></section>{{end}}<p>{{.Quiz.Description}}</p><div class="grid"><section class="card"><form id="quizForm" method="post" action="/finish">
{{range $i,$q:=.Quiz.Questions}}<div class="question" data-q="{{$q.ID}}"><h3>{{add $i 1}}. {{$q.Text}}</h3>{{range $j,$option:=$q.Options}}<label class="option"><input type="radio" name="answer_{{$q.ID}}" value="{{$j}}" required> {{$option}}</label>{{end}}<input type="hidden" name="time_{{$q.ID}}" value="0"></div>{{end}}
<div class="question"><h3>Насколько сильное напряжение вы испытывали во время теста?</h3><p class="muted">1 — совсем не испытывал(а), 7 — очень сильное.</p><div class="stress">{{range seq7}}<label><input type="radio" name="stress" value="{{.}}" required> {{.}}</label>{{end}}</div></div>
<button type="submit">Завершить тест</button></form></section>
</div>
<script>
const started=Date.now();document.querySelectorAll('[data-q]').forEach(el=>{const input=el.querySelector('input[type=hidden]');el.querySelectorAll('input[type=radio]').forEach(r=>r.addEventListener('change',()=>{input.value=String(Date.now()-started)}))});
</script>
{{end}}</main></body></html>`
