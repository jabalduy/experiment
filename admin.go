package main

import (
 "context"
 "encoding/json"
 "strconv"
 "strings"
 "crypto/subtle"
 "fmt"
 "html/template"
 "net/http"
 "os"
 "net/url"
 "time"
)

type adminGroup struct {
 Name string
 Started, Finished int
 AvgScore, AvgBefore, AvgAfter, AvgChange, AvgSeconds float64
}
type adminQuestion struct { ID, Label string; RankingCorrect,RankingTotal,ControlCorrect,ControlTotal int }
type adminParticipant struct { ID int64; Name,Group,Source string; Started,Finished string; Score,Before,After string; Seconds,Answers string }

func adminAuthorized(r *http.Request) bool {
 expected:=os.Getenv("ADMIN_KEY")
 if expected=="" {return false}
 provided:=r.Header.Get("X-Admin-Key")
 if provided=="" {_,password,ok:=r.BasicAuth();if ok {provided=password}}
 return subtle.ConstantTimeCompare([]byte(expected),[]byte(provided))==1
}
func adminRequire(w http.ResponseWriter,r *http.Request) bool {
 if adminAuthorized(r) {return true}
 w.Header().Set("WWW-Authenticate",`Basic realm="Experiment analytics", charset="UTF-8"`)
 w.Header().Set("Cache-Control","no-store")
 http.Error(w,"Требуется пароль администратора (ADMIN_KEY).",http.StatusUnauthorized)
 return false
}
func (a *App) adminDashboard(w http.ResponseWriter,r *http.Request) {
 if !adminRequire(w,r) {return}
 ctx,cancel:=context.WithTimeout(r.Context(),10*time.Second);defer cancel()
 groups,err:=a.adminGroups(ctx);if err!=nil {http.Error(w,"Ошибка статистики",500);return}
 questions,err:=a.adminQuestions(ctx);if err!=nil {http.Error(w,"Ошибка вопросов",500);return}
 people,err:=a.adminParticipants(ctx);if err!=nil {http.Error(w,"Ошибка участников",500);return}
 w.Header().Set("Cache-Control","no-store")
 w.Header().Set("Referrer-Policy","no-referrer")
 w.Header().Set("X-Content-Type-Options","nosniff")
 w.Header().Set("Content-Type","text/html; charset=utf-8")
 scheme:="https";if r.Header.Get("X-Forwarded-Proto")=="http" {scheme="http"} else if r.TLS==nil&&r.Header.Get("X-Forwarded-Proto")=="" {scheme="http"}
 base:=scheme+"://"+r.Host
 var randomCount,manualCount int
 if err=a.db.QueryRow(ctx,"SELECT count(*) FILTER (WHERE assignment_source='random')::int,count(*) FILTER (WHERE assignment_source='manual')::int FROM attempts").Scan(&randomCount,&manualCount);err!=nil {http.Error(w,"Ошибка статистики",500);return}
 fmt.Fprint(w,`<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Аналитика · Experiment</title><link rel="stylesheet" href="/static/admin.css"></head><body><main><h1>Аналитика эксперимента</h1><p>Описательные показатели по завершённым тестам. Незаполненные группы не интерпретируются как нулевой результат.</p><section class="card"><h2>Сравнение групп</h2><div class="scroll"><table><thead><tr><th>Группа</th><th>Зарегистрированы</th><th>Завершили</th><th>Средний балл</th><th>До (20–80)</th><th>После (20–80)</th><th>Δ тревожности</th><th>Время, с</th></tr></thead><tbody>`)
 for _,g:=range groups {
  fmt.Fprintf(w,"<tr><th>%s</th><td>%d</td><td>%d</td>",template.HTMLEscapeString(groupLabel(g.Name)),g.Started,g.Finished)
  if g.Finished==0 {fmt.Fprint(w,"<td colspan=5>Нет завершённых тестов</td></tr>");continue}
  fmt.Fprintf(w,"<td>%.2f / %d</td><td>%.2f</td><td>%.2f</td><td>%+.2f</td><td>%.1f</td></tr>",g.AvgScore,len(a.quiz.Questions),g.AvgBefore,g.AvgAfter,g.AvgChange,g.AvgSeconds)
 }
 fmt.Fprint(w,`</tbody></table></div><p><strong>Основная выборка:</strong> `,randomCount,` / 60 регистраций. <strong>Дополнительные приглашения:</strong> `,manualCount,`.</p><h3>Средний балл</h3>`)
 for _,g:=range groups {if g.Finished>0 {fmt.Fprintf(w,"<div class='bar-row'><span>%s · %.2f</span><div class='bar-track'><div class='bar' style='width:%.2f%%'></div></div></div>",template.HTMLEscapeString(groupLabel(g.Name)),g.AvgScore,100*g.AvgScore/float64(len(a.quiz.Questions)))}}
 fmt.Fprint(w,`</section>`)
 fmt.Fprint(w,`<section class="card"><h2>Ссылки для приглашения</h2><p><strong>Обычная ссылка:</strong> случайное сбалансированное распределение, 30/30 при 60 регистрациях. Регистрация занимает место, даже если анкета осталась незавершённой. После 60 регистраций набор по общей ссылке закрывается.</p>`)
 invite:=func(label,link string){fmt.Fprintf(w,`<div class="invite-box"><strong>%s</strong><input aria-label="%s" readonly value="%s" onclick="this.select()"><button type="button" onclick="navigator.clipboard.writeText(this.previousElementSibling.value)">Скопировать</button></div>`,template.HTMLEscapeString(label),template.HTMLEscapeString(label),template.HTMLEscapeString(link))}
 invite("Общая ссылка — случайное распределение",base+"/")
 fmt.Fprint(w,`<p><strong>Дополнительные ссылки:</strong> используйте только для исключений и добора участников. Они сразу назначают нужную группу, не занимают места в основной квоте и работают после достижения 60 регистраций. Не публикуйте их открыто. Такие участники помечаются в статистике как «Вручную».</p>`)
 invite("Контрольная группа — без рейтинга",base+"/join/control?key="+url.QueryEscape(invitationKey("control")))
 invite("Экспериментальная группа — с рейтингом",base+"/join/ranking?key="+url.QueryEscape(invitationKey("ranking")))
 fmt.Fprint(w,`<p>Уже зарегистрированный участник в том же браузере продолжит свою прежнюю попытку, даже если откроет другую ссылку. Для независимой проверки используйте приватное окно. Дополнительные ссылки перестанут работать после изменения ADMIN_KEY.</p></section><section class="card"><h2>Памятка исследователям</h2><ol><li>Отправляйте основную ссылку студентам. Участник не выбирает группу — её определяет система.</li><li>Участник принимает согласие, вводит только имя, читает инструкцию своего условия и заполняет 20 утверждений тревожности.</li><li>Далее выполняет пять одинаковых логических заданий за 10 минут. Контрольная группа видит личный балл, экспериментальная — балл и рейтинг.</li><li>После результата участник повторно заполняет 20 утверждений, затем видит благодарность.</li><li>Если участник зарегистрировался, но не закончил, его место в основной квоте уже занято. Для компенсации можно отправить дополнительную ссылку на нужную группу.</li><li>Для научного анализа отдельно учитывайте вручную назначенных участников: их распределение не было случайным.</li></ol><h3>Как читать статистику</h3><p><strong>Зарегистрированы</strong> — создали попытку; <strong>Завершили</strong> — отправили задания (повторная шкала может быть ещё не заполнена). <strong>Средний балл</strong> — правильные ответы из пяти. <strong>До / После</strong> — сумма баллов тревожности по 20 утверждениям (20–80, больше означает выше тревожность). <strong>Δ</strong> = после минус до; положительное значение означает рост тревожности. <strong>Время</strong> — секунды от начала заданий до отправки.</p><p>Проценты правильных ответов по вопросам помогают сравнить сложность задач для двух групп. CSV содержит индивидуальные ответы, баллы и способ назначения группы. Для анализа полных анкет исключайте записи без повторной шкалы.</p><p><strong>Ограничение:</strong> в текущем порядке повторная шкала заполняется после показа результата/рейтинга. Изменение тревожности может отражать реакцию на результат. Средние показатели сами по себе не доказывают статистическую значимость.</p></section>`)

 fmt.Fprint(w,`<section class="card"><h2>Правильные ответы по вопросам</h2><div class="scroll"><table><thead><tr><th>Вопрос</th><th>Соревнование</th><th>Контроль</th></tr></thead><tbody>`)
 for _,q:=range questions {
  fmt.Fprintf(w,"<tr><td>%s</td><td>%s</td><td>%s</td></tr>",template.HTMLEscapeString(q.Label),percent(q.RankingCorrect,q.RankingTotal),percent(q.ControlCorrect,q.ControlTotal))
 }
 fmt.Fprint(w,`</tbody></table></div></section><section class="card"><h2>Участники (последние 200)</h2><p>Псевдонимы отображаются только в административной панели.</p><div class="scroll"><table><thead><tr><th>ID</th><th>Псевдоним</th><th>Группа</th><th>Назначение</th><th>Баллы</th><th>Ответы (1–5)</th><th>До</th><th>После</th><th>Время, с</th><th>Статус</th></tr></thead><tbody>`)
 for _,p:=range people {fmt.Fprintf(w,"<tr><td>%d</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>",p.ID,template.HTMLEscapeString(p.Name),template.HTMLEscapeString(groupLabel(p.Group)),template.HTMLEscapeString(p.Source),p.Score,template.HTMLEscapeString(p.Answers),p.Before,p.After,p.Seconds,p.Finished)}
 fmt.Fprint(w,`</tbody></table></div></section><section class="card"><h2>Экспорт</h2><p>CSV со всеми попытками и ответами: <code>/admin/export</code>. Для скачивания используйте заголовок <code>X-Admin-Key</code> либо Basic Auth с паролем ADMIN_KEY. Доступ не предоставляется по ссылке с ключом в URL.</p></section><p>Различия средних не доказывают причинный эффект или статистическую значимость.</p></main></body></html>`)
}
func groupLabel(g string) string {if g=="ranking" {return "Соревнование"};if g=="control" {return "Контроль"};return g}
func percent(ok,total int) string {if total==0 {return "—"};return fmt.Sprintf("%.1f%% (%d/%d)",100*float64(ok)/float64(total),ok,total)}
func (a *App) adminGroups(ctx context.Context)([]adminGroup,error) {
 rows,err:=a.db.Query(ctx,`SELECT grp,count(*)::int,count(*) FILTER (WHERE finished_at IS NOT NULL)::int,
 COALESCE(avg(score) FILTER (WHERE finished_at IS NOT NULL),0)::float8,
 COALESCE(avg(pre_score) FILTER (WHERE finished_at IS NOT NULL),0)::float8,
 COALESCE(avg(post_score) FILTER (WHERE finished_at IS NOT NULL),0)::float8,
 COALESCE(avg(post_score-pre_score) FILTER (WHERE finished_at IS NOT NULL),0)::float8,
 COALESCE(avg(extract(epoch from finished_at-test_started_at)) FILTER (WHERE finished_at IS NOT NULL AND test_started_at IS NOT NULL),0)::float8
 FROM attempts GROUP BY grp ORDER BY grp`)
 if err!=nil{return nil,err};defer rows.Close()
 out:=[]adminGroup{}
 for rows.Next(){var g adminGroup;if err=rows.Scan(&g.Name,&g.Started,&g.Finished,&g.AvgScore,&g.AvgBefore,&g.AvgAfter,&g.AvgChange,&g.AvgSeconds);err!=nil{return nil,err};out=append(out,g)}
 return out,rows.Err()
}
func (a *App) adminQuestions(ctx context.Context)([]adminQuestion,error) {
 rows,err:=a.db.Query(ctx,`SELECT b.question_id,
 count(*) FILTER (WHERE a.grp='ranking' AND b.correct)::int,
 count(*) FILTER (WHERE a.grp='ranking')::int,
 count(*) FILTER (WHERE a.grp='control' AND b.correct)::int,
 count(*) FILTER (WHERE a.grp='control')::int
 FROM answers b JOIN attempts a ON a.id=b.attempt_id WHERE a.finished_at IS NOT NULL GROUP BY b.question_id`)
 if err!=nil{return nil,err};defer rows.Close()
 byID:=map[string]adminQuestion{}
 for rows.Next(){var q adminQuestion;if err=rows.Scan(&q.ID,&q.RankingCorrect,&q.RankingTotal,&q.ControlCorrect,&q.ControlTotal);err!=nil{return nil,err};byID[q.ID]=q}
 if err=rows.Err();err!=nil{return nil,err}
 out:=make([]adminQuestion,0,len(a.quiz.Questions))
 for i,q:=range a.quiz.Questions {v:=byID[q.ID];v.Label=fmt.Sprintf("%d. %s",i+1,q.Text);out=append(out,v)}
 return out,nil
}
func (a *App) adminParticipants(ctx context.Context)([]adminParticipant,error) {
 rows,err:=a.db.Query(ctx,`SELECT id,name,grp,assignment_source,finished_at IS NOT NULL,score,pre_score,post_score,
 CASE WHEN finished_at IS NOT NULL AND test_started_at IS NOT NULL THEN extract(epoch from finished_at-test_started_at)::float8 END,
 (SELECT jsonb_object_agg(b.question_id,jsonb_build_object('choice',b.choice,'raw',b.raw_answer)) FROM answers b WHERE b.attempt_id=attempts.id)::text
 FROM attempts ORDER BY id DESC LIMIT 200`)
 if err!=nil{return nil,err};defer rows.Close()
 out:=[]adminParticipant{}
 for rows.Next(){var p adminParticipant;var done bool;var score,before,after *int;var duration *float64;var answerJSON *string
 if err=rows.Scan(&p.ID,&p.Name,&p.Group,&p.Source,&done,&score,&before,&after,&duration,&answerJSON);err!=nil{return nil,err}
 if p.Source=="manual" {p.Source="Вручную"} else {p.Source="Случайно"}
 p.Answers="—";if answerJSON!=nil {var responses map[string]struct{Choice int `json:"choice"`;Raw *string `json:"raw"`};if json.Unmarshal([]byte(*answerJSON),&responses)==nil {parts:=make([]string,0,len(a.quiz.Questions));for i,q:=range a.quiz.Questions {value:="—";if response,ok:=responses[q.ID];ok {if response.Raw!=nil {value=strings.TrimSpace(*response.Raw)} else if q.Input==""&&response.Choice>=0&&response.Choice<len(q.Options) {value=q.Options[response.Choice]} else if q.Input!="" {value="(старый ответ не сохранён)"};if value=="" {value="—"}};parts=append(parts,strconv.Itoa(i+1)+": "+value)};p.Answers=strings.Join(parts," · ")}}
 p.Score="—";p.Before="—";p.After="—";p.Seconds="—";p.Finished="Не завершён"
 if before!=nil {p.Before=fmt.Sprint(*before)}
 if score!=nil {p.Score=fmt.Sprint(*score)}
 if after!=nil {p.After=fmt.Sprint(*after)}
 if duration!=nil {p.Seconds=fmt.Sprintf("%.1f",*duration)}
 if done {p.Finished="Завершён"}
 out=append(out,p)}
 return out,rows.Err()
}
