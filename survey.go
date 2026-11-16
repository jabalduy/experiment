package main

import (
 "encoding/json"
 "errors"
 "fmt"
 "net/http"
 "strconv"
)

// Twenty state-anxiety statements from the participant-supplied Russian form.
// The first line in the source image is numbered 2; statement numbering here is 1–20.
var surveyItems = []string{
 "Я спокоен",
 "Мне ничто не угрожает",
 "Я нахожусь в напряжении",
 "Я испытываю сожаление",
 "Я чувствую себя свободно",
 "Я расстроен",
 "Меня волнуют возможные неудачи",
 "Я чувствую себя отдохнувшим",
 "Я встревожен",
 "Я испытываю чувство внутреннего удовлетворения",
 "Я уверен в себе",
 "Я нервничаю",
 "Я не нахожу себе места",
 "Я взвинчен",
 "Я не чувствую скованности, напряженности",
 "Я доволен",
 "Я озабочен",
 "Я слишком возбужден и мне не по себе",
 "Мне радостно",
 "Мне приятно",
}

var surveyPositive = map[int]bool{1:true,2:true,5:true,8:true,10:true,11:true,15:true,16:true,19:true,20:true}

func parseSurvey(r *http.Request,prefix string)([]byte,int,error) {
 if err:=r.ParseForm();err!=nil {return nil,0,err}
 answers:=make([]int,len(surveyItems));score:=0
 for i:=range surveyItems {
  v,err:=strconv.Atoi(r.FormValue(fmt.Sprintf("%s%d",prefix,i+1)))
  if err!=nil||v<1||v>4 {return nil,0,errors.New("Ответьте на все 20 утверждений (варианты 1–4)")}
  answers[i]=v
  if surveyPositive[i+1] {score+=5-v} else {score+=v}
 }
 b,err:=json.Marshal(answers)
 return b,score,err
}

func (a *App) survey(w http.ResponseWriter,r *http.Request) {
 at,err:=a.current(r);if err!=nil {http.Redirect(w,r,"/",303);return}
 if !at.Finished {http.Redirect(w,r,"/",303);return}
 var done bool
 if err=a.db.QueryRow(r.Context(),"SELECT post_answers IS NOT NULL FROM attempts WHERE id=$1",at.ID).Scan(&done);err!=nil {http.Error(w,"Database error",500);return}
 if done {http.Redirect(w,r,"/thanks",303);return}
 a.render(w,PageData{Quiz:a.quiz,Attempt:at,Total:-2,SurveyItems:surveyItems,SurveyMode:"post"})
}
func (a *App) submitSurvey(w http.ResponseWriter,r *http.Request) {
 at,err:=a.current(r);if err!=nil {http.Error(w,"Unauthorized",401);return}
 if !at.Finished {http.Error(w,"Test not finished",409);return}
 answers,score,err:=parseSurvey(r,"post_");if err!=nil {http.Error(w,err.Error(),400);return}
 if _,err=a.db.Exec(r.Context(),"UPDATE attempts SET post_answers=COALESCE(post_answers,$2::jsonb),post_score=COALESCE(post_score,$3) WHERE id=$1 AND finished_at IS NOT NULL",at.ID,string(answers),score);err!=nil {http.Error(w,"Database error",500);return}
 http.Redirect(w,r,"/thanks",303)
}
func (a *App) thanks(w http.ResponseWriter,r *http.Request) {
 at,err:=a.current(r);if err!=nil {http.Redirect(w,r,"/",303);return}
 var done bool
 if err=a.db.QueryRow(r.Context(),"SELECT post_answers IS NOT NULL FROM attempts WHERE id=$1",at.ID).Scan(&done);err!=nil {http.Error(w,"Database error",500);return}
 if !at.Finished {http.Redirect(w,r,"/",303);return}
 if !done {http.Redirect(w,r,"/survey",303);return}
 a.render(w,PageData{Quiz:a.quiz,Attempt:at,Total:-3})
}
