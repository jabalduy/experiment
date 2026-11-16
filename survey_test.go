package main

import (
 "net/http/httptest"
 "net/url"
 "strings"
 "testing"
)

func TestStateAnxietyScoring(t *testing.T) {
 if len(surveyItems)!=20 {t.Fatalf("expected 20 statements, got %d",len(surveyItems))}
 for _,v:=range []struct{choice,want int}{{1,50},{2,50},{3,50},{4,50}} {
  form:=url.Values{}
  for i:=1;i<=20;i++ {form.Set("pre_"+itoa(i),itoa(v.choice))}
  req:=httptest.NewRequest("POST","/begin",strings.NewReader(form.Encode()))
  req.Header.Set("Content-Type","application/x-www-form-urlencoded")
  answers,score,err:=parseSurvey(req,"pre_")
  if err!=nil||score!=v.want||len(answers)==0 {t.Fatalf("choice %d: score %d, error %v",v.choice,score,err)}
 }
 form:=url.Values{}
 for i:=1;i<=20;i++ {form.Set("post_"+itoa(i),"2")}
 form.Del("post_20")
 req:=httptest.NewRequest("POST","/survey",strings.NewReader(form.Encode()))
 req.Header.Set("Content-Type","application/x-www-form-urlencoded")
 if _,_,err:=parseSurvey(req,"post_");err==nil {t.Fatal("missing answer accepted")}
}
func itoa(n int)string {return string([]byte{byte('0'+n/10),byte('0'+n%10)})}
