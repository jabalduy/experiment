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
type Attempt struct { ID int64; Name, Group string; Finished bool; Score int; TestStarted bool }
type Row struct { Name string `json:"name"`; Score int `json:"score"`; Total int `json:"total"` }
type PageData struct { Quiz Quiz; Attempt Attempt; Ranking []Row; Error string; Total int; JoinPath string }

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
 ranking_seen BOOLEAN NOT NULL DEFAULT FALSE,
 test_started_at TIMESTAMPTZ
 );
 CREATE TABLE IF NOT EXISTS answers (
 attempt_id BIGINT NOT NULL REFERENCES attempts(id),
 question_id TEXT NOT NULL, choice INT NOT NULL, correct BOOLEAN NOT NULL,
 elapsed_ms INT NOT NULL DEFAULT 0, PRIMARY KEY(attempt_id,question_id)
 );`
 if _,err=db.Exec(ctx,schema);err!=nil {log.Fatal(err)}
 if _,err=db.Exec(ctx,`ALTER TABLE attempts ADD COLUMN IF NOT EXISTS baseline_stress INT CHECK(baseline_stress BETWEEN 1 AND 7); ALTER TABLE attempts ADD COLUMN IF NOT EXISTS ranking_seen BOOLEAN NOT NULL DEFAULT FALSE; ALTER TABLE attempts ADD COLUMN IF NOT EXISTS test_started_at TIMESTAMPTZ`);err!=nil {log.Fatal(err)}
 a:=&App{db:db,quiz:q,page:template.Must(template.New("page").Funcs(template.FuncMap{"add":func(a,b int)int{return a+b},"seq7":func()[]int{return []int{1,2,3,4,5,6,7}}}).Parse(htmlPage))}
 mux:=http.NewServeMux()
 mux.HandleFunc("GET /",a.home)
 mux.HandleFunc("GET /join/{group}",a.join)
 mux.HandleFunc("POST /join/{group}/start",a.start)
 mux.HandleFunc("POST /start",a.start)
 mux.HandleFunc("GET /briefing",a.briefing)
 mux.HandleFunc("POST /begin",a.begin)
 mux.HandleFunc("GET /test",a.test)
 mux.HandleFunc("POST /finish",a.finish)
 mux.HandleFunc("GET /result",a.result)
 mux.HandleFunc("GET /api/leaderboard",a.leaderboard)
 mux.HandleFunc("GET /admin/export",a.export)
 mux.HandleFunc("GET /admin",a.admin)
 mux.Handle("GET /static/",http.StripPrefix("/static/",http.FileServer(http.Dir("static"))))
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
 if at.ID!=0 {if at.Finished {http.Redirect(w,r,"/result",http.StatusSeeOther)} else if at.TestStarted {http.Redirect(w,r,"/test",http.StatusSeeOther)} else {http.Redirect(w,r,"/briefing",http.StatusSeeOther)};return}
 a.render(w,PageData{Quiz:a.quiz,JoinPath:"/start"})
}
func (a *App) join(w http.ResponseWriter,r *http.Request) {
 group:=r.PathValue("group")
 if group!="ranking" && group!="control" {http.NotFound(w,r);return}
 at,_:=a.current(r)
 if at.ID!=0 {a.home(w,r);return}
 a.render(w,PageData{Quiz:a.quiz,JoinPath:"/join/"+group+"/start"})
}
func token() (string,error) {b:=make([]byte,32);if _,err:=rand.Read(b);err!=nil{return "",err};return hex.EncodeToString(b),nil}
func (a *App) start(w http.ResponseWriter,r *http.Request) {
 if at,_:=a.current(r);at.ID!=0 {if at.Finished {http.Redirect(w,r,"/result",303)} else if at.TestStarted {http.Redirect(w,r,"/test",303)} else {http.Redirect(w,r,"/briefing",303)};return}
 if err:=r.ParseForm();err!=nil {http.Error(w,"Bad form",400);return}
 name:=strings.TrimSpace(r.FormValue("name"))
 joinPath:="/start";if group:=r.PathValue("group");group=="ranking"||group=="control" {joinPath="/join/"+group+"/start"}
 if len([]rune(name))<2||len([]rune(name))>60||r.FormValue("consent")!="yes" {a.render(w,PageData{Quiz:a.quiz,JoinPath:joinPath,Error:"Введите имя (2–60 символов) и подтвердите согласие."});return}
 t,err:=token();if err!=nil {http.Error(w,"Server error",500);return}
 ctx:=r.Context()
 tx,err:=a.db.Begin(ctx);if err!=nil {http.Error(w,"Database error",500);return}
 defer tx.Rollback(ctx)
 // Serialize assignment to keep the two conditions balanced.
 if _,err=tx.Exec(ctx,"SELECT pg_advisory_xact_lock(894211)");err!=nil {http.Error(w,"Database error",500);return}
 var nr,nc int
 err=tx.QueryRow(ctx,"SELECT count(*) FILTER (WHERE grp='ranking'), count(*) FILTER (WHERE grp='control') FROM attempts").Scan(&nr,&nc)
 if err!=nil {http.Error(w,"Database error",500);return}
 group:=r.PathValue("group")
 if group!="ranking" && group!="control" {group="ranking";if nr>nc {group="control"} else if nr==nc {var b [1]byte;if _,err=rand.Read(b[:]);err!=nil {http.Error(w,"Server error",500);return};if b[0]%2==0 {group="control"}}}
 _,err=tx.Exec(ctx,"INSERT INTO attempts(token,name,grp) VALUES($1,$2,$3)",t,name,group)
 if err!=nil {http.Error(w,"Database error",500);return}
 if err=tx.Commit(ctx);err!=nil {http.Error(w,"Database error",500);return}
 http.SetCookie(w,&http.Cookie{Name:"experiment_session",Value:t,Path:"/",HttpOnly:true,SameSite:http.SameSiteLaxMode,Secure:r.TLS!=nil,MaxAge:86400})
 http.Redirect(w,r,"/briefing",http.StatusSeeOther)
}
func (a *App) current(r *http.Request) (Attempt,error) {
 c,err:=r.Cookie("experiment_session");if err!=nil {return Attempt{},err}
 var at Attempt
 err=a.db.QueryRow(r.Context(),"SELECT id,name,grp,finished_at IS NOT NULL,COALESCE(score,0),test_started_at IS NOT NULL FROM attempts WHERE token=$1",c.Value).Scan(&at.ID,&at.Name,&at.Group,&at.Finished,&at.Score,&at.TestStarted)
 return at,err
}
func (a *App) briefing(w http.ResponseWriter,r *http.Request) {
 at,err:=a.current(r);if err!=nil {http.Redirect(w,r,"/",303);return}
 if at.Finished {http.Redirect(w,r,"/result",303);return}
 if at.TestStarted {http.Redirect(w,r,"/test",303);return}
 a.render(w,PageData{Quiz:a.quiz,Attempt:at,Total:-1})
}
func (a *App) begin(w http.ResponseWriter,r *http.Request) {
 at,err:=a.current(r);if err!=nil {http.Error(w,"Unauthorized",401);return}
 if at.Finished {http.Redirect(w,r,"/result",303);return}
 stress,err:=strconv.Atoi(r.FormValue("baseline_stress"));if err!=nil||stress<1||stress>7 {http.Error(w,"Оцените напряжение от 1 до 7 перед началом теста",400);return}
 if _,err=a.db.Exec(r.Context(),"UPDATE attempts SET baseline_stress=COALESCE(baseline_stress,$2),test_started_at=COALESCE(test_started_at,now()) WHERE id=$1 AND finished_at IS NULL",at.ID,stress);err!=nil {http.Error(w,"Database error",500);return}
 http.Redirect(w,r,"/test",303)
}
func (a *App) test(w http.ResponseWriter,r *http.Request) {
 at,err:=a.current(r);if err!=nil {http.Redirect(w,r,"/",303);return}
 if at.Finished {http.Redirect(w,r,"/result",303);return}
 if !at.TestStarted {http.Redirect(w,r,"/briefing",303);return}
 // Copy questions without correct answer indices to avoid exposing answer keys.
 q:=a.quiz;q.Questions=append([]Question(nil),q.Questions...)
 for i:=range q.Questions {q.Questions[i].Correct=-1}
 a.render(w,PageData{Quiz:q,Attempt:at,Total:len(q.Questions)})
}
func (a *App) finish(w http.ResponseWriter,r *http.Request) {
 at,err:=a.current(r);if err!=nil {http.Error(w,"Unauthorized",401);return}
 if at.Finished {http.Redirect(w,r,"/result",303);return}
 if !at.TestStarted {http.Error(w,"Test not started",409);return}
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
 tag,err:=tx.Exec(ctx,"UPDATE attempts SET finished_at=now(),score=$1,stress=$2 WHERE id=$3 AND finished_at IS NULL AND test_started_at IS NOT NULL",score,stress,at.ID)
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
 if !at.Finished {if at.TestStarted {http.Redirect(w,r,"/test",303)} else {http.Redirect(w,r,"/briefing",303)};return}
 data:=PageData{Quiz:a.quiz,Attempt:at,Total:len(a.quiz.Questions)}
 if at.Group=="ranking" {data.Ranking,err=a.getRanking(r.Context());if err!=nil {http.Error(w,"Database error",500);return}}
 a.render(w,data)
}

func (a *App) admin(w http.ResponseWriter,r *http.Request) { a.adminDashboard(w,r) }

func (a *App) export(w http.ResponseWriter,r *http.Request) {
 if !adminRequire(w,r) {return}
 rows,err:=a.db.Query(r.Context(),`SELECT a.id,a.name,a.grp,a.started_at,a.finished_at,a.test_started_at,a.score,a.stress,a.baseline_stress,a.ranking_seen,
 COALESCE(b.question_id,''),b.choice,b.correct,b.elapsed_ms
 FROM attempts a LEFT JOIN answers b ON b.attempt_id=a.id ORDER BY a.id,b.question_id`)
 if err!=nil {http.Error(w,"Database error",500);return};defer rows.Close()
 w.Header().Set("Content-Type","text/csv; charset=utf-8");w.Header().Set("Content-Disposition","attachment; filename=experiment.csv")
 writer:=csv.NewWriter(w);defer writer.Flush()
 writer.Write([]string{"attempt_id","name","group","registered_at","finished_at","test_started_at","score","stress_1_7","baseline_stress_1_7","ranking_seen","question_id","choice_index","correct","elapsed_ms"})
 for rows.Next() {
  var id int64;var name,group,start,qid string;var end,testStarted *time.Time;var score,stress,baseline,choice,elapsed *int;var correct *bool;var rankingSeen bool
  var started time.Time
  if err=rows.Scan(&id,&name,&group,&started,&end,&testStarted,&score,&stress,&baseline,&rankingSeen,&qid,&choice,&correct,&elapsed);err!=nil {log.Println(err);return}
  start=started.Format(time.RFC3339)
  writer.Write([]string{fmt.Sprint(id),name,group,start,fmtTime(end),fmtTime(testStarted),fmtPtr(score),fmtPtr(stress),fmtPtr(baseline),strconv.FormatBool(rankingSeen),qid,fmtPtr(choice),fmtBool(correct),fmtPtr(elapsed)})
 }
 if err=rows.Err();err!=nil {log.Println(err)}
}
func fmtPtr(v *int) string {if v==nil{return ""};return strconv.Itoa(*v)}
func fmtBool(v *bool) string {if v==nil{return ""};return strconv.FormatBool(*v)}
func fmtTime(v *time.Time) string {if v==nil{return ""};return v.Format(time.RFC3339)}

const htmlPage = `<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="theme-color" content="#f5f7fb"><title>{{.Quiz.Title}}</title><link rel="stylesheet" href="/static/style.css"></head><body><div class="shell"><header class="top"><div class="brand"><span class="brand-mark">E</span> Experiment</div><span class="tag">Учебное исследование</span></header><main>
{{if eq .Attempt.ID 0}}
<section class="panel"><span class="eyebrow">Шаг 01 · Участие</span><h1>{{.Quiz.Title}}</h1><p class="lead">{{.Quiz.Description}}</p><p>Это учебное исследование способов прохождения тестов. Участие добровольное. Мы сохраним ваши ответы, время и оценки напряжения для анализа. Используйте псевдоним: в одном из условий он будет показан другим участникам после тестирования.</p>{{if .Error}}<p role="alert" class="error">{{.Error}}</p>{{end}}
<form method="post" action="{{.JoinPath}}"><hr class="divider"><label class="field-title" for="name">Ваш псевдоним</label><input class="text-input" id="name" name="name" maxlength="60" minlength="2" required autocomplete="nickname" placeholder="Например, Студент 12">

<label class="consent"><input type="checkbox" name="consent" value="yes" required><span>Я добровольно соглашаюсь участвовать и разрешаю сохранить мои ответы и оценки для учебного анализа.</span></label><div class="actions"><button class="btn" type="submit">Продолжить →</button></div></form></section>
{{else if .Attempt.Finished}}
<section class="panel"><span class="eyebrow">Тест завершён</span><h1>Спасибо за участие!</h1><p>Ваши ответы сохранены. Спасибо, {{.Attempt.Name}}.</p><div class="results"><div class="score">{{.Attempt.Score}} <small>/ {{.Total}}</small></div><p>Правильных ответов<br><strong>Ваш итоговый результат</strong></p></div>
{{if eq .Attempt.Group "ranking"}}<hr class="divider"><h2>Таблица лидеров</h2><p>Результаты участников соревновательной группы. При равных баллах выше находится тот, кто завершил тест раньше.</p><ol class="leaderboard">{{range $i,$row:=.Ranking}}<li><span><span class="place">{{add $i 1}}.</span>{{$row.Name}}</span><strong>{{$row.Score}} / {{$row.Total}}</strong></li>{{else}}<li>Пока нет результатов</li>{{end}}</ol>{{else}}<div class="note"><p>На этом прохождение завершено. Благодарим за участие в исследовании.</p></div>{{end}}</section>
{{else if eq .Total -1}}
<section class="panel"><span class="eyebrow">Шаг 02 · Инструкция</span><h1>Перед началом теста</h1>
{{if eq .Attempt.Group "ranking"}}<div class="note"><h2>Соревновательный режим</h2><p><strong>Вы соревнуетесь с другими студентами.</strong> Ваш результат войдёт в общую таблицу лидеров. После завершения теста вы увидите своё место среди остальных участников.</p></div>{{else}}<p class="lead">Вам предстоит ответить на вопросы теста. Ваш личный результат будет показан после завершения.</p>{{end}}
<hr class="divider"><p>Отвечайте самостоятельно. После нажатия кнопки начнётся отсчёт времени выполнения заданий.</p><form action="/begin" method="post"><h3>Какое напряжение вы испытываете сейчас?</h3><p class="muted">Оцените своё состояние после прочтения инструкции, но до начала вопросов.</p><div class="scale" role="group" aria-label="Напряжение перед тестом от 1 до 7">{{range seq7}}<label><input type="radio" name="baseline_stress" value="{{.}}" required><span>{{.}}</span></label>{{end}}</div><div class="scale-ends"><span>1 · Совсем нет</span><span>7 · Очень сильное</span></div><div class="actions"><button class="btn" type="submit">Начать отвечать →</button></div></form></section>
{{else}}
<div class="quiz-top"><div><span class="eyebrow">Шаг 03 · Вопросы</span><h1>{{.Quiz.Title}}</h1></div><span class="tag"><span id="answered">0</span> / {{.Total}} ответов</span></div><div class="progress" role="progressbar" aria-label="Прогресс теста" aria-valuemin="0" aria-valuemax="{{.Total}}" aria-valuenow="0" id="progress"><div id="progressFill"></div></div><p>{{.Quiz.Description}}</p>
<form id="quizForm" method="post" action="/finish">{{range $i,$q:=.Quiz.Questions}}<fieldset class="question" data-q="{{$q.ID}}"><legend class="question-num">Вопрос {{add $i 1}}</legend><h3>{{$q.Text}}</h3>{{range $j,$option:=$q.Options}}<label class="option"><input type="radio" name="answer_{{$q.ID}}" value="{{$j}}" required><span>{{$option}}</span></label>{{end}}<input type="hidden" name="time_{{$q.ID}}" value="0"></fieldset>{{end}}
<section class="question"><span class="eyebrow">Последний шаг</span><h3>Насколько сильное напряжение вы испытывали во время теста?</h3><p class="muted">1 — совсем не испытывали, 7 — очень сильное.</p><div class="scale" role="group" aria-label="Напряжение во время теста от 1 до 7">{{range seq7}}<label><input type="radio" name="stress" value="{{.}}" required><span>{{.}}</span></label>{{end}}</div><div class="scale-ends"><span>1 · Совсем нет</span><span>7 · Очень сильное</span></div></section>
<div class="actions"><button class="btn" id="submitBtn" type="submit">Завершить тест →</button></div></form>
<script>
(function(){
const form=document.getElementById('quizForm');const questions=Array.from(form.querySelectorAll('[data-q]'));const started=Date.now();const progress=document.getElementById('progress');const fill=document.getElementById('progressFill');const count=document.getElementById('answered');const total=questions.length;
function update(){let done=0;questions.forEach(q=>{if(q.querySelector('input[type=radio]:checked'))done++});count.textContent=done;fill.style.width=(done/total*100)+'%';progress.setAttribute('aria-valuenow',done)}
questions.forEach(q=>{const elapsed=q.querySelector('input[type=hidden]');q.querySelectorAll('input[type=radio]').forEach(r=>r.addEventListener('change',()=>{elapsed.value=String(Date.now()-started);update()}))});
form.addEventListener('submit',e=>{if(!form.checkValidity()){e.preventDefault();form.reportValidity();return}document.getElementById('submitBtn').disabled=true});
update();
})();
</script>
{{end}}</main><footer class="foot">Experiment · Учебное исследование</footer></div></body></html>`
