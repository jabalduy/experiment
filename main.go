package main

import (
 "context"
 "crypto/rand"
 "crypto/hmac"
 "crypto/sha256"
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

type Question struct { ID string `json:"id"`; Text string `json:"text"`; Options []string `json:"options"`; Input string `json:"input,omitempty"`; Answer string `json:"answer,omitempty"`; Correct int `json:"correct"` }
type Quiz struct { ID string `json:"id"`; Title string `json:"title"`; Description string `json:"description"`; Questions []Question `json:"questions"` }
type App struct { db *pgxpool.Pool; quiz Quiz; page *template.Template }
type Attempt struct { ID int64; Name, Group, Code string; Finished bool; Score int; TestStarted bool; Ready bool; Named bool; DeadlineUnix int64; ServerNowUnix int64 }
type Row struct { Name string `json:"name"`; Score int `json:"score"`; Total int `json:"total"`; Demo bool `json:"demo"` }
type PageData struct { Quiz Quiz; Attempt Attempt; Ranking []Row; Error string; Total int; JoinPath string; InviteGroup string; InviteKey string; SurveyItems []string; SurveyMode string; SurveyError string; SurveyDone bool }

func main() {
 b,err:=os.ReadFile("quizzes/experiment.json"); if err!=nil {log.Fatal(err)}
 var q Quiz; if err=json.Unmarshal(b,&q);err!=nil {log.Fatal(err)}
 if len(q.Questions)==0 {log.Fatal("quiz must have questions")}
 seen:=map[string]bool{}
 for _,v:=range q.Questions {if v.ID==""||seen[v.ID]||(v.Input==""&&(len(v.Options)<2||v.Correct<0||v.Correct>=len(v.Options)))||(v.Input!=""&&(v.Answer==""||(v.Input!="number"&&v.Input!="letter"))) {log.Fatal("invalid quiz question: ",v.ID)};seen[v.ID]=true}
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
 test_started_at TIMESTAMPTZ,
 ready_at TIMESTAMPTZ,
 pre_answers JSONB,post_answers JSONB,pre_score INT,post_score INT,
 assignment_source TEXT NOT NULL DEFAULT 'random', name_set BOOLEAN NOT NULL DEFAULT TRUE, participant_code TEXT UNIQUE
 );
 CREATE TABLE IF NOT EXISTS demo_participants (
 id SERIAL PRIMARY KEY,
 name VARCHAR(40) NOT NULL,
 score INTEGER NOT NULL CHECK (score BETWEEN 0 AND 5),
 created_at TIMESTAMPTZ DEFAULT NOW()
 );
 CREATE TABLE IF NOT EXISTS answers (
 attempt_id BIGINT NOT NULL REFERENCES attempts(id),
 question_id TEXT NOT NULL, choice INT NOT NULL, raw_answer TEXT, correct BOOLEAN NOT NULL,
 elapsed_ms INT NOT NULL DEFAULT 0, PRIMARY KEY(attempt_id,question_id)
 );`
 if _,err=db.Exec(ctx,schema);err!=nil {log.Fatal(err)}
 if _,err=db.Exec(ctx,`ALTER TABLE attempts ADD COLUMN IF NOT EXISTS participant_code TEXT; CREATE UNIQUE INDEX IF NOT EXISTS attempts_code_unique ON attempts(participant_code); ALTER TABLE attempts ADD COLUMN IF NOT EXISTS name_set BOOLEAN NOT NULL DEFAULT TRUE; ALTER TABLE answers ADD COLUMN IF NOT EXISTS raw_answer TEXT; ALTER TABLE attempts ADD COLUMN IF NOT EXISTS baseline_stress INT CHECK(baseline_stress BETWEEN 1 AND 7); ALTER TABLE attempts ADD COLUMN IF NOT EXISTS ranking_seen BOOLEAN NOT NULL DEFAULT FALSE; ALTER TABLE attempts ADD COLUMN IF NOT EXISTS test_started_at TIMESTAMPTZ; ALTER TABLE attempts ADD COLUMN IF NOT EXISTS ready_at TIMESTAMPTZ; ALTER TABLE attempts ADD COLUMN IF NOT EXISTS pre_answers JSONB; ALTER TABLE attempts ADD COLUMN IF NOT EXISTS post_answers JSONB; ALTER TABLE attempts ADD COLUMN IF NOT EXISTS pre_score INT; ALTER TABLE attempts ADD COLUMN IF NOT EXISTS post_score INT; ALTER TABLE attempts ADD COLUMN IF NOT EXISTS assignment_source TEXT NOT NULL DEFAULT 'random'`);err!=nil {log.Fatal(err)}
 a:=&App{db:db,quiz:q,page:template.Must(template.New("page").Funcs(template.FuncMap{"add":func(a,b int)int{return a+b},"seq4":func()[]int{return []int{1,2,3,4}}}).Parse(htmlPage))}
 mux:=http.NewServeMux()
 mux.HandleFunc("GET /",a.home)
 mux.HandleFunc("GET /join/{group}",a.join)
 mux.HandleFunc("POST /start",a.start)
 mux.HandleFunc("GET /briefing",a.briefing)
 mux.HandleFunc("POST /begin",a.begin)
 mux.HandleFunc("GET /before",a.before)
 mux.HandleFunc("POST /before",a.proceedBefore)
 mux.HandleFunc("GET /code",a.codePage)
 mux.HandleFunc("GET /ready",a.ready)
 mux.HandleFunc("POST /launch",a.launch)
 mux.HandleFunc("GET /test",a.test)
 mux.HandleFunc("POST /finish",a.finish)
 mux.HandleFunc("GET /result",a.result)
 mux.HandleFunc("GET /survey",a.survey)
 mux.HandleFunc("POST /survey",a.submitSurvey)
 mux.HandleFunc("GET /thanks",a.thanks)
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
 if at.ID!=0 {if at.Finished {http.Redirect(w,r,"/result",http.StatusSeeOther)} else if at.TestStarted {http.Redirect(w,r,"/test",http.StatusSeeOther)} else if at.Ready {http.Redirect(w,r,"/code",http.StatusSeeOther)} else {http.Redirect(w,r,"/briefing",http.StatusSeeOther)};return}
 a.render(w,PageData{Quiz:a.quiz,JoinPath:"/start"})
}
func invitationKey(group string) string {
 secret:=os.Getenv("ADMIN_KEY")
 if secret=="" {return ""}
 mac:=hmac.New(sha256.New,[]byte(secret))
 mac.Write([]byte("experiment-invite-v1:"+group))
 return hex.EncodeToString(mac.Sum(nil))
}
func validInvitation(group,key string) bool {
 expected:=invitationKey(group)
 return expected!="" && hmac.Equal([]byte(expected),[]byte(key))
}
func (a *App) join(w http.ResponseWriter,r *http.Request) {
 group:=r.PathValue("group")
 if group!="ranking"&&group!="control" {http.NotFound(w,r);return}
 key:=r.URL.Query().Get("key")
 if !validInvitation(group,key) {http.NotFound(w,r);return}
 if at,_:=a.current(r);at.ID!=0 {http.Redirect(w,r,"/",303);return}
 w.Header().Set("Cache-Control","no-store")
 a.render(w,PageData{Quiz:a.quiz,JoinPath:"/start",InviteGroup:group,InviteKey:key})
}
func token() (string,error) {b:=make([]byte,32);if _,err:=rand.Read(b);err!=nil{return "",err};return hex.EncodeToString(b),nil}
func (a *App) start(w http.ResponseWriter,r *http.Request) {
 if at,_:=a.current(r);at.ID!=0 {if at.Finished {http.Redirect(w,r,"/result",303)} else if at.TestStarted {http.Redirect(w,r,"/test",303)} else if at.Ready {http.Redirect(w,r,"/code",303)} else {http.Redirect(w,r,"/briefing",303)};return}
 if err:=r.ParseForm();err!=nil {http.Error(w,"Bad form",400);return}
 name:="Участник"
 joinPath:="/start"
 inviteGroup:=r.FormValue("invite_group")
 inviteKey:=r.FormValue("invite_key")
 forced:=validInvitation(inviteGroup,inviteKey) && (inviteGroup=="ranking"||inviteGroup=="control")
 if inviteGroup!=""&&!forced {http.Error(w,"Недействительная ссылка приглашения",403);return}
 if r.FormValue("consent")!="yes" {a.render(w,PageData{Quiz:a.quiz,JoinPath:joinPath,InviteGroup:inviteGroup,InviteKey:inviteKey,Error:"Введите только имя (без фамилии) и подтвердите согласие."});return}
 t,err:=token();if err!=nil {http.Error(w,"Server error",500);return}
 ctx:=r.Context()
 tx,err:=a.db.Begin(ctx);if err!=nil {http.Error(w,"Database error",500);return}
 defer tx.Rollback(ctx)
 // Serialize assignment to keep the two conditions balanced.
 if _,err=tx.Exec(ctx,"SELECT pg_advisory_xact_lock(894211)");err!=nil {http.Error(w,"Database error",500);return}
 var nr,nc int
 err=tx.QueryRow(ctx,"SELECT count(*) FILTER (WHERE grp='ranking'), count(*) FILTER (WHERE grp='control') FROM attempts WHERE assignment_source='random'").Scan(&nr,&nc)
 if err!=nil {http.Error(w,"Database error",500);return}
 group:="ranking"
 source:="random"
 if forced {
  group=inviteGroup
  source="manual"
 } else {
  if nr+nc>=60 {http.Error(w,"Набор по общей ссылке завершён (60 участников). Обратитесь к организаторам исследования.",409);return}
  if nr>nc {group="control"} else if nr==nc {var b [1]byte;if _,err=rand.Read(b[:]);err!=nil {http.Error(w,"Server error",500);return};if b[0]%2==0 {group="control"}}
 }
 var attemptID int64
 err=tx.QueryRow(ctx,"INSERT INTO attempts(token,name,grp,assignment_source,name_set) VALUES($1,$2,$3,$4,TRUE) RETURNING id",t,name,group,source).Scan(&attemptID)
 if err!=nil {http.Error(w,"Database error",500);return}
 if group=="ranking" {
  if attemptID>9000 {http.Error(w,"Лимит кодов исчерпан",503);return}
  code:=fmt.Sprintf("К-%04d",1000+(attemptID*7919+381)%9000)
  if _,err=tx.Exec(ctx,"UPDATE attempts SET participant_code=$2 WHERE id=$1",attemptID,code);err!=nil {http.Error(w,"Database error",500);return}
 }
 if err=tx.Commit(ctx);err!=nil {http.Error(w,"Database error",500);return}
 http.SetCookie(w,&http.Cookie{Name:"experiment_session",Value:t,Path:"/",HttpOnly:true,SameSite:http.SameSiteLaxMode,Secure:r.TLS!=nil,MaxAge:86400})
 http.Redirect(w,r,"/briefing",http.StatusSeeOther)
}
func (a *App) current(r *http.Request) (Attempt,error) {
 c,err:=r.Cookie("experiment_session");if err!=nil {return Attempt{},err}
 var at Attempt
 err=a.db.QueryRow(r.Context(),"SELECT id,name,grp,finished_at IS NOT NULL,COALESCE(score,0),test_started_at IS NOT NULL,ready_at IS NOT NULL,name_set,COALESCE(participant_code,'') FROM attempts WHERE token=$1",c.Value).Scan(&at.ID,&at.Name,&at.Group,&at.Finished,&at.Score,&at.TestStarted,&at.Ready,&at.Named,&at.Code)
 return at,err
}
func (a *App) briefing(w http.ResponseWriter,r *http.Request) {
 at,err:=a.current(r);if err!=nil {http.Redirect(w,r,"/",303);return}
 if at.Finished {http.Redirect(w,r,"/result",303);return}
 if at.TestStarted {http.Redirect(w,r,"/test",303);return}
 a.render(w,PageData{Quiz:a.quiz,Attempt:at,Total:-1})
}
func (a *App) begin(w http.ResponseWriter,r *http.Request) {
 at,err:=a.current(r);if err!=nil {http.Redirect(w,r,"/",303);return}
 if at.Finished {http.Redirect(w,r,"/result",303);return}
 if at.TestStarted {http.Redirect(w,r,"/test",303);return}
 http.Redirect(w,r,"/before",303)
}
func (a *App) before(w http.ResponseWriter,r *http.Request) {
 at,err:=a.current(r);if err!=nil {http.Redirect(w,r,"/",303);return}
 if at.Finished {http.Redirect(w,r,"/result",303);return}
 if at.TestStarted {http.Redirect(w,r,"/test",303);return}
 a.render(w,PageData{Quiz:a.quiz,Attempt:at,Total:-4})
}
func (a *App) proceedBefore(w http.ResponseWriter,r *http.Request) {
 at,err:=a.current(r);if err!=nil {http.Redirect(w,r,"/",303);return}
 if at.Finished {http.Redirect(w,r,"/result",303);return}
 if at.TestStarted {http.Redirect(w,r,"/test",303);return}
 if at.Group=="ranking" {http.Redirect(w,r,"/code",303);return}
 a.launch(w,r)
}
func (a *App) codePage(w http.ResponseWriter,r *http.Request) {
 at,err:=a.current(r);if err!=nil {http.Redirect(w,r,"/",303);return}
 if at.Finished {http.Redirect(w,r,"/result",303);return}
 if at.TestStarted {http.Redirect(w,r,"/test",303);return}
 if at.Group!="ranking" {http.Redirect(w,r,"/briefing",303);return}
 a.render(w,PageData{Quiz:a.quiz,Attempt:at,Total:-5})
}
func (a *App) ready(w http.ResponseWriter,r *http.Request) {
 http.Redirect(w,r,"/before",303)
}
func (a *App) launch(w http.ResponseWriter,r *http.Request) {
 at,err:=a.current(r);if err!=nil {http.Error(w,"Unauthorized",401);return}
 if at.Finished {http.Redirect(w,r,"/result",303);return}
 if _,err=a.db.Exec(r.Context(),"UPDATE attempts SET ready_at=COALESCE(ready_at,clock_timestamp()),test_started_at=COALESCE(test_started_at,clock_timestamp()) WHERE id=$1 AND finished_at IS NULL",at.ID);err!=nil {http.Error(w,"Database error",500);return}
 http.Redirect(w,r,"/test",303)
}
func (a *App) test(w http.ResponseWriter,r *http.Request) {
 at,err:=a.current(r);if err!=nil {http.Redirect(w,r,"/",303);return}
 if at.Finished {http.Redirect(w,r,"/result",303);return}
 if !at.Ready {http.Redirect(w,r,"/before",303);return}
 // Copy questions without correct answer indices to avoid exposing answer keys.
 q:=a.quiz;q.Questions=append([]Question(nil),q.Questions...)
 for i:=range q.Questions {q.Questions[i].Correct=-1;q.Questions[i].Answer=""}
 if !at.TestStarted {http.Redirect(w,r,"/ready",303);return}
 if err=a.db.QueryRow(r.Context(),"SELECT (extract(epoch from test_started_at + interval '10 minutes')*1000)::bigint, (extract(epoch from clock_timestamp())*1000)::bigint FROM attempts WHERE id=$1",at.ID).Scan(&at.DeadlineUnix,&at.ServerNowUnix);err!=nil {http.Error(w,"Database error",500);return}
 w.Header().Set("Cache-Control","no-store")
 a.render(w,PageData{Quiz:q,Attempt:at,Total:len(q.Questions)})
}
func (a *App) finish(w http.ResponseWriter,r *http.Request) {
 at,err:=a.current(r);if err!=nil {http.Error(w,"Unauthorized",401);return}
 if at.Finished {http.Redirect(w,r,"/result",303);return}
 if !at.TestStarted {http.Error(w,"Test not started",409);return}
 if err=r.ParseForm();err!=nil {http.Error(w,"Invalid form",400);return}
 var expired bool
 if err=a.db.QueryRow(r.Context(),"SELECT clock_timestamp() >= test_started_at + interval '10 minutes' FROM attempts WHERE id=$1",at.ID).Scan(&expired);err!=nil {http.Error(w,"Database error",500);return}
 type answer struct {id,raw string;choice,elapsed int;correct bool}
 items:=make([]answer,0,len(a.quiz.Questions));score:=0
 for _,q:=range a.quiz.Questions {
  v:=r.FormValue("answer_"+q.ID);choice:=-1;good:=false
  if q.Input!="" {
   if strings.TrimSpace(v)==""&&!expired {http.Error(w,"Ответьте на все пять заданий",400);return}
   normalized:=strings.TrimSpace(v)
   if q.Input=="letter" {normalized=strings.ToUpper(normalized)}
   if normalized!="" {good=normalized==q.Answer}
  } else {if v=="" {if !expired {http.Error(w,"Ответьте на все пять заданий",400);return}} else {n,e:=strconv.Atoi(v);if e!=nil||n<0||n>=len(q.Options) {http.Error(w,"Некорректный ответ",400);return};choice=n;good=choice==q.Correct}}
  ms,_:=strconv.Atoi(r.FormValue("time_"+q.ID));if ms<0||ms>3600000 {ms=0}
  if good {score++}
  items=append(items,answer{q.ID,v,choice,ms,good})
 }
 ctx:=r.Context();tx,err:=a.db.Begin(ctx);if err!=nil {http.Error(w,"Database error",500);return};defer tx.Rollback(ctx)
 tag,err:=tx.Exec(ctx,"UPDATE attempts SET finished_at=now(),score=$1 WHERE id=$2 AND finished_at IS NULL AND test_started_at IS NOT NULL",score,at.ID)
 if err!=nil {http.Error(w,"Database error",500);return}
 if tag.RowsAffected()==0 {http.Redirect(w,r,"/result",303);return}
 for _,v:=range items {if _,err=tx.Exec(ctx,"INSERT INTO answers(attempt_id,question_id,choice,correct,elapsed_ms,raw_answer) VALUES($1,$2,$3,$4,$5,$6)",at.ID,v.id,v.choice,v.correct,v.elapsed,v.raw);err!=nil {http.Error(w,"Database error",500);return}}
 if err=tx.Commit(ctx);err!=nil {http.Error(w,"Database error",500);return}
 http.Redirect(w,r,"/result",303)
}
func (a *App) getRanking(ctx context.Context) ([]Row,error) {
 // Demo rows live in a separate table and never enter attempts, exports or recruitment counts.
 rows,err:=a.db.Query(ctx,`SELECT name,score FROM (
   SELECT COALESCE(participant_code,name) AS name,score,finished_at AS sort_time FROM attempts
     WHERE grp='ranking' AND finished_at IS NOT NULL
   UNION ALL
   SELECT name,score,created_at AS sort_time FROM demo_participants
 ) ranking_rows ORDER BY score DESC,sort_time ASC,name ASC LIMIT 50`)
 if err!=nil{return nil,err}
 defer rows.Close()
 out:=[]Row{}
 for rows.Next() {
  var v Row
  if err=rows.Scan(&v.Name,&v.Score);err!=nil{return nil,err}
  v.Total=len(a.quiz.Questions)
  out=append(out,v)
 }
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
 if !at.Finished {if at.TestStarted {http.Redirect(w,r,"/test",303)} else if at.Ready {http.Redirect(w,r,"/ready",303)} else {http.Redirect(w,r,"/briefing",303)};return}
 data:=PageData{Quiz:a.quiz,Attempt:at,Total:len(a.quiz.Questions)}
 if at.Group=="ranking" {data.Ranking,err=a.getRanking(r.Context());if err!=nil {http.Error(w,"Database error",500);return}}
 var postDone bool
 if err=a.db.QueryRow(r.Context(),"SELECT post_answers IS NOT NULL FROM attempts WHERE id=$1",at.ID).Scan(&postDone);err!=nil {http.Error(w,"Database error",500);return}
 data.SurveyDone=postDone
 a.render(w,data)
}

func (a *App) admin(w http.ResponseWriter,r *http.Request) { a.adminDashboard(w,r) }

func (a *App) export(w http.ResponseWriter,r *http.Request) {
 if !adminRequire(w,r) {return}
 rows,err:=a.db.Query(r.Context(),`SELECT a.id,a.name,a.grp,a.started_at,a.finished_at,a.test_started_at,a.score,a.post_score,a.pre_score,a.ranking_seen,a.pre_answers::text,a.post_answers::text,a.assignment_source,
 COALESCE(b.question_id,''),b.choice,b.correct,b.elapsed_ms,b.raw_answer
 FROM attempts a LEFT JOIN answers b ON b.attempt_id=a.id ORDER BY a.id,b.question_id`)
 if err!=nil {http.Error(w,"Database error",500);return};defer rows.Close()
 w.Header().Set("Content-Type","text/csv; charset=utf-8");w.Header().Set("Content-Disposition","attachment; filename=experiment.csv")
 writer:=csv.NewWriter(w);defer writer.Flush()
 writer.Write([]string{"attempt_id","name","group","registered_at","finished_at","test_started_at","score","post_anxiety_20_80","pre_anxiety_20_80","ranking_seen","pre_answers_1_4","post_answers_1_4","assignment_source","question_id","choice_index","correct","elapsed_ms","raw_answer"})
 for rows.Next() {
  var id int64;var name,group,start,qid string;var end,testStarted *time.Time;var score,stress,baseline,choice,elapsed *int;var correct *bool;var rankingSeen bool;var preAnswers,postAnswers,rawAnswer *string;var assignmentSource string
  var started time.Time
  if err=rows.Scan(&id,&name,&group,&started,&end,&testStarted,&score,&stress,&baseline,&rankingSeen,&preAnswers,&postAnswers,&assignmentSource,&qid,&choice,&correct,&elapsed,&rawAnswer);err!=nil {log.Println(err);return}
  start=started.Format(time.RFC3339)
  writer.Write([]string{fmt.Sprint(id),name,group,start,fmtTime(end),fmtTime(testStarted),fmtPtr(score),fmtPtr(stress),fmtPtr(baseline),strconv.FormatBool(rankingSeen),fmtStr(preAnswers),fmtStr(postAnswers),assignmentSource,qid,fmtPtr(choice),fmtBool(correct),fmtPtr(elapsed),fmtStr(rawAnswer)})
 }
 if err=rows.Err();err!=nil {log.Println(err)}
}
func fmtStr(v *string) string {if v==nil{return ""};return *v}
func fmtPtr(v *int) string {if v==nil{return ""};return strconv.Itoa(*v)}
func fmtBool(v *bool) string {if v==nil{return ""};return strconv.FormatBool(*v)}
func fmtTime(v *time.Time) string {if v==nil{return ""};return v.Format(time.RFC3339)}

const htmlPage = `<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="theme-color" content="#f5f7fb"><title>{{.Quiz.Title}}</title><link rel="stylesheet" href="/static/style.css"></head><body><div class="shell"><header class="top"><div class="brand"><span class="brand-mark">E</span> Experiment</div><span class="tag">Учебное исследование</span></header><main>
{{if eq .Attempt.ID 0}}
<section class="panel"><span class="eyebrow">Шаг 01 · Информированное согласие</span><h1>Участие в исследовании</h1><p class="lead">Перед началом участия просим ознакомиться со следующей информацией:</p>
<ol class="consent-info"><li>Участие в исследовании является полностью добровольным. Вы можете отказаться от участия или прекратить его в любой момент без объяснения причин и каких-либо негативных последствий.</li><li>Собранные данные будут использоваться исключительно в учебных и научно-исследовательских целях.</li><li>После завершения исследования вы можете получить информацию о его целях и проверяемой гипотезе, обратившись к организаторам исследования. Контакты для связи: Евгения — lasevgenia@yandex.ru (почта), Таисия — @taissiyasn (тг).</li></ol>
{{if .Error}}<p role="alert" class="error">{{.Error}}</p>{{end}}
<form method="post" action="/start" data-complete-form="consent">{{if .InviteGroup}}<input type="hidden" name="invite_group" value="{{.InviteGroup}}"><input type="hidden" name="invite_key" value="{{.InviteKey}}">{{end}}<hr class="divider"><label class="consent"><input type="checkbox" name="consent" value="yes" required><span><strong>Подтверждение согласия.</strong> Нажимая «Согласен(на) участвовать», я подтверждаю, что ознакомился(-ась) с представленной информацией, понимаю характер и условия исследования и добровольно соглашаюсь принять в нём участие.</span></label><div class="actions"><button class="btn" type="submit">Согласен(на) участвовать →</button></div></form></section>
{{else if eq .Total -3}}
<section class="panel thanks"><span class="eyebrow">Исследование завершено</span><h1>Благодарим за участие в нашем исследовании!</h1><p>Спасибо, ваши ответы сохранены.</p></section>
{{else if eq .Total -2}}
<section class="panel"><span class="eyebrow">Шаг 04 · Оценка состояния</span><h1>Как вы чувствуете себя сейчас?</h1><div class="survey-intro"><p>Теперь, пожалуйста, оцените представленные ниже утверждения, опираясь на то, как вы чувствуете себя в данный момент.</p><p>Для каждого утверждения выберите один из четырёх вариантов ответа:</p><p><strong>1</strong> — Нет, это не так<br><strong>2</strong> — Пожалуй, так<br><strong>3</strong> — Верно<br><strong>4</strong> — Совершенно верно</p><p>Пожалуйста, отвечайте искренне, ориентируясь на своё текущее состояние.</p></div><form action="/survey" method="post" data-complete-form="survey"><div class="survey-list">{{range $i,$item:=.SurveyItems}}<fieldset class="survey-item"><legend><strong>{{add $i 1}}.</strong> {{$item}}</legend><div class="scale four">{{range seq4}}<label><input type="radio" name="post_{{add $i 1}}" value="{{.}}" required><span>{{.}}</span></label>{{end}}</div></fieldset>{{end}}</div><div class="actions"><button class="btn" type="submit">Завершить исследование →</button></div></form></section>
{{else if .Attempt.Finished}}
<section class="panel"><span class="eyebrow">Шаг 03 · Результат</span><h1>Ваш результат</h1><div class="results"><div class="score">{{.Attempt.Score}} <small>/ {{.Total}}</small></div><p>Правильных ответов</p></div>
{{if eq .Attempt.Group "ranking"}}<p class="muted">Ваш код участника: <strong>{{.Attempt.Code}}</strong></p><hr class="divider"><h2>Таблица лидеров</h2><p>При равных баллах выше находится тот, кто завершил тест раньше.</p><ol class="leaderboard">{{range $i,$row:=.Ranking}}<li{{if and $.Attempt.Code (eq $row.Name $.Attempt.Code)}} class="leaderboard-self"{{end}}><span><span class="place">{{add $i 1}}.</span>{{$row.Name}}{{if and $.Attempt.Code (eq $row.Name $.Attempt.Code)}} <small>(Вы)</small>{{end}}</span><strong>{{$row.Score}} / {{$row.Total}}</strong></li>{{else}}<li>Пока нет результатов</li>{{end}}</ol>{{end}}
{{if not .SurveyDone}}<hr class="divider"><p>Остался последний этап — оценить своё текущее состояние.</p><div class="actions"><a class="btn" href="/survey">Продолжить →</a></div>{{else}}<div class="actions"><a class="btn" href="/thanks">К благодарности →</a></div>{{end}}</section>
{{else if eq .Total -1}}
<section class="panel"><span class="eyebrow">Шаг 02 · Инструкция</span><h1>Инструкция</h1>
{{if eq .Attempt.Group "ranking"}}<p>Вам предстоит выполнить 5 заданий на логическое мышление и поиск закономерностей за ограниченное время. Ваш результат будет сопоставлен с результатами других участников. После выполнения задания вы сможете ознакомиться с распределением результатов между всеми участниками в рейтинге. Постарайтесь выполнить задания как можно точнее.</p>{{else}}<p>Вам предстоит выполнить 5 заданий на логическое мышление и поиск закономерностей за ограниченное время. Вы увидите свой результат после выполнения заданий, и он будет использован только в рамках исследования. Постарайтесь выполнить задания как можно точнее.</p>{{end}}
<form action="/begin" method="post"><div class="actions"><button class="btn" type="submit">Продолжить →</button></div></form></section>
{{else if eq .Total -5}}
<section class="panel"><span class="eyebrow">Шаг 03 · Код участника</span><h1>Ваш код участника</h1><p>Запомните или запишите этот код. По нему вы сможете найти свой результат в рейтинге.</p><div style="text-align:center;padding:36px 12px;margin:24px 0;background:#eef2ff;border:1px solid #c7d2fe;border-radius:18px"><strong style="font-size:clamp(42px,9vw,76px);letter-spacing:.07em;color:#3458df">{{.Attempt.Code}}</strong></div><p class="muted">Код закреплён за вами и не меняется. Таймер начнётся после нажатия кнопки.</p><form method="post" action="/launch"><div class="actions"><button class="btn" type="submit">Начать задания →</button></div></form></section>
{{else if eq .Total -4}}
<section class="panel"><span class="eyebrow">Шаг 03 · Перед заданиями</span><h1>Перед началом</h1><p>Пожалуйста, выполняйте задания самостоятельно, без использования искусственного интеллекта, поисковых систем и помощи других людей.</p><p>При необходимости можно пользоваться бумагой и ручкой для записей и вычислений.</p><p>Если задание покажется сложным, постарайтесь найти решение самостоятельно.</p><p><strong>Желаем удачи!</strong></p><form method="post" action="/before"><div class="actions"><button class="btn" type="submit">Продолжить →</button></div></form></section>
{{else}}
<div class="quiz-top"><div><span class="eyebrow">Шаг 03 · Задания</span><h1>Логические задачи</h1></div><span class="tag"><span id="answered">0</span> / {{.Total}} ответов</span></div>
<section class="panel"><div class="timer" id="timer" role="timer" aria-live="off">10:00</div><p class="muted">Через 10 минут ответы отправятся автоматически. Если не знаете ответ, можно оставить вопрос пустым.</p></section>
<div class="progress" role="progressbar" aria-label="Прогресс теста" aria-valuemin="0" aria-valuemax="{{.Total}}" aria-valuenow="0" id="progress"><div id="progressFill"></div></div>
<form id="quizForm" method="post" action="/finish">{{range $i,$q:=.Quiz.Questions}}<fieldset class="question" data-q="{{$q.ID}}"><legend class="question-num">Задание {{add $i 1}}</legend><h3>{{$q.Text}}</h3>{{if $q.Input}}<label class="field-title" for="answer_{{$q.ID}}">Введите только {{if eq $q.Input "number"}}число{{else}}букву{{end}}</label><input class="text-input" type="text" id="answer_{{$q.ID}}" name="answer_{{$q.ID}}" autocomplete="off" inputmode="{{if eq $q.Input "number"}}numeric{{else}}text{{end}}">{{else}}{{range $j,$option:=$q.Options}}<label class="option"><input type="radio" name="answer_{{$q.ID}}" value="{{$j}}"><span>{{$option}}</span></label>{{end}}{{end}}<input type="hidden" name="time_{{$q.ID}}" value="0"></fieldset>{{end}}<div class="actions"><button class="btn" id="submitBtn" type="submit" disabled>Завершить задания →</button></div></form>
<script>(function(){const form=document.getElementById('quizForm'),questions=Array.from(form.querySelectorAll('[data-q]')),started=Date.now(),progress=document.getElementById('progress'),fill=document.getElementById('progressFill'),count=document.getElementById('answered'),timer=document.getElementById('timer'),deadline=Number("{{.Attempt.DeadlineUnix}}"),serverNow=Number("{{.Attempt.ServerNowUnix}}"),loadedAt=performance.now();let sent=false;function update(){let done=0;questions.forEach(q=>{if(q.querySelector('input[type=radio]:checked')||(q.querySelector('input[type=text]')&&q.querySelector('input[type=text]').value.trim()))done++});count.textContent=done;fill.style.width=(done/questions.length*100)+'%';progress.setAttribute('aria-valuenow',done);document.getElementById('submitBtn').disabled=done!==questions.length||sent}questions.forEach(q=>{const elapsed=q.querySelector('input[type=hidden]');q.querySelectorAll('input[type=radio],input[type=text]').forEach(r=>r.addEventListener(r.type==='text'?'input':'change',()=>{elapsed.value=String(Date.now()-started);update()}))});function tick(){if(!deadline)return;const left=Math.max(0,Math.ceil((deadline-(serverNow+performance.now()-loadedAt))/1000));timer.textContent=String(Math.floor(left/60)).padStart(2,'0')+':'+String(left%60).padStart(2,'0');if(left===0&&!sent){sent=true;form.submit()}}form.addEventListener('submit',event=>{if(questions.some(q=>!q.querySelector('input[type=radio]:checked')&&!(q.querySelector('input[type=text]')&&q.querySelector('input[type=text]').value.trim()))){event.preventDefault();return}sent=true;document.getElementById('submitBtn').disabled=true});update();tick();setInterval(tick,200)})();</script>
{{end}}</main><script>(function(){document.querySelectorAll("form[data-complete-form]").forEach(form=>{const button=form.querySelector("button[type=submit]");if(!button)return;function update(){let complete;if(form.dataset.completeForm==="consent"){complete=!!form.querySelector('input[name="consent"]:checked')}else if(form.dataset.completeForm==="name"){complete=!!form.querySelector('input[name="name"]').value.trim()}else{const fields=form.querySelectorAll(".survey-item");complete=fields.length===20&&Array.from(fields).every(field=>!!field.querySelector("input[type=radio]:checked"))}button.disabled=!complete}form.addEventListener("input",update);form.addEventListener("change",update);form.addEventListener("submit",event=>{update();if(button.disabled)event.preventDefault()});update()})})();</script><footer class="foot">Учебное исследование · Спасибо за участие</footer></div></body></html>`