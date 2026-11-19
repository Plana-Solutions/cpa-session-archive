package archive

import (
 "context"
 "encoding/json"
 "fmt"
 "net/http"
 "net/url"
 "os"
 "strings"
 "testing"
 "time"
)

func TestRound7Regressions(t *testing.T) {
 c := localClickHouse(t)
 for _, file := range []string{"schema.sql", "derived.sql"} {
  b, err := os.ReadFile("testdata/clickhouse/"+file); if err != nil {t.Fatal(err)}
  for _, sql := range strings.Split(string(b), "\n;\n") {chExec(t,c,strings.ReplaceAll(sql,"{db}","cliproxy"))}
 }
 seed := func(id string, texts, roles []string, extra map[string]any) {
  at:=time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05.999999999")
  directions, types := make([]string,len(texts)),make([]string,len(texts))
  for i:=range texts {directions[i]="input";types[i]="message"}
  row:=map[string]any{"request_id":id,"conversation_id":id,"started_at":at,"ingested_at":at,"kind":"messages","input_items":200,"last_user_text":texts[len(texts)-1],"msg_text":texts,"msg_role":roles,"msg_direction":directions,"msg_type":types}
  for k,v:=range extra {row[k]=v}; chInsert(t,c,"request_facts",row)
 }
 seed("first-with-tools",[]string{"I regretted the choice and want to fix it","tool result"},[]string{"user","tool"},nil)
 seed("multiline",[]string{"Perform an operation on it.\n\n## 1.\tConnect the archive"},[]string{"user"},nil)
 seed("title-generator",[]string{"Generate a title that will help the user recognize this T3 Code thread weeks later.","<skill>Extra instructions</skill>"},[]string{"user","user"},nil)
 seed("real-mention",[]string{"Please check Generate a title that will help the user recognize this T3 Code thread"},[]string{"user"},nil)
 for _,v:=range []struct{term,id string}{{"regretted the choice","first-with-tools"},{"operation on it. ## 1. Connect","multiline"},{"operation\non it.  ## 1.\tConnect","multiline"}} {
  t.Run(v.id+v.term,func(t *testing.T){got,err:=c.read("sessions",url.Values{"q":{v.term}});if err!=nil {t.Fatal(err)}; rows:=got.([]archiveSession);if len(rows)!=1||rows[0].SessionID!=v.id {t.Fatalf("search %q got %+v",v.term,rows)}})
 }
 t.Run("system items",func(t *testing.T){
  got,err:=c.read("sessions",nil);if err!=nil {t.Fatal(err)}
  found:=false;for _,r:=range got.([]archiveSession){if r.SessionID=="title-generator" {t.Error("title generator visible")};found=found||r.SessionID=="real-mention"};if !found {t.Error("real session hidden")}
 })
 t.Run("missing session while continuing record",func(t *testing.T){
  for _,q:=range []url.Values{{"scope":{"all"},"session_offset":{"100"},"byte_offset":{"1"}},{"id":{"deleted"},"byte_offset":{"1"}},{"id":{"multiline"},"offset":{"100"},"byte_offset":{"1"}}} {
   if got,err:=c.read("export",q);err==nil {t.Errorf("partial export silently ended %+v",got)}
  }
 })
}

func TestRound7Preambles(t *testing.T) {
 for _,v:=range []struct{raw,want string}{
  {"Some of the conversation history has been summarized to free up context.\nYou were originally given instructions. Here were the user messages:\n<user_message>\nFix the archive titles\n</user_message>\nHere is a summary: private", "Fix the archive titles"},
  {"<system>Immutable security policy. "+strings.Repeat("injected ",800)+"</system>\nFix the archive titles","Fix the archive titles"},
  {"Some of the conversation history has been summarized to free up context.\n<user_message><system>Immutable security policy.</system>Fix the archive titles</user_message>\n<summary>old</summary>","Fix the archive titles"},
  {"Some of the conversation history has been summarized to free up context.",""},
  {"<system>Unclosed policy",""},
  {"Discuss <system> tags in the docs","Discuss <system> tags in the docs"},
 } {
  if got:=meaningfulUserText(v.raw);got!=v.want {t.Errorf("title got %q want %q",got,v.want)}
  if got:=cleanTurnText(v.raw);got!=v.want {t.Errorf("turn got %q want %q",got,v.want)}
 }
}

func round7LiveReader(t *testing.T) *clickhouseReader {
 t.Helper();if os.Getenv("ARCHIVE_R7_LIVE")=="" {t.Skip("opt-in read-only production checks")}
 return &clickhouseReader{config:pluginConfig{URL:os.Getenv("CLICKHOUSE_URL"),Database:"cliproxy",User:os.Getenv("CLICKHOUSE_USER"),Password:os.Getenv("CLICKHOUSE_PASSWORD")},client:&http.Client{Timeout:60*time.Second}}
}

func TestRound7LiveTitles(t *testing.T) {
 c:=round7LiveReader(t)
 out,err:=c.read("sessions",url.Values{"days":{"7"},"limit":{"1000"},"show_system":{"1"}});if err!=nil {t.Fatal(err)}
 rows:=out.([]archiveSession)
 if path:=os.Getenv("ARCHIVE_R7_SNAPSHOT");path!="" {
  b,_:=json.Marshal(rows);if err:=os.WriteFile(path,b,0600);err!=nil {t.Fatal(err)};t.Logf("snapshot sessions=%d",len(rows));return
 }
 for _,term:=range []string{"regretted the choice","kegt to do","gimme the url","operation on it. ## 1. Connect"} {
  out,err:=c.read("sessions",url.Values{"q":{term},"limit":{"1000"}});if err!=nil {t.Fatal(err)}
  ids:=[]string{};for _,r:=range out.([]archiveSession){ids=append(ids,r.SessionID)};t.Logf("EXAMPLE term=%q ids=%v",term,ids);if len(ids)==0 {t.Errorf("example missing %q",term)};time.Sleep(time.Second)
 }
 before:=map[string]string{}
 if path:=os.Getenv("ARCHIVE_R7_BEFORE");path!="" {b,err:=os.ReadFile(path);if err!=nil {t.Fatal(err)};var old []archiveSession;if err=json.Unmarshal(b,&old);err!=nil {t.Fatal(err)};for _,r:=range old {before[r.SessionID]=r.Summary}}
 hits,misses,empty,changed,system:=0,0,0,0,0
 for _,r:=range rows {
  if old,ok:=before[r.SessionID];ok&&old!=r.Summary {changed++}
  if r.System {system++;continue}
  words:=strings.Fields(strings.TrimSuffix(r.Summary,"…"));if len(words)==0 {empty++;t.Logf("FIND id=%s empty title",r.SessionID);continue}
  phrase:=strings.Join(words[:min(6,len(words))]," ")
  out,err:=c.read("sessions",url.Values{"q":{phrase},"limit":{"1000"}})
  found:=false;if err==nil {for _,v:=range out.([]archiveSession){found=found||v.SessionID==r.SessionID}}
  if found {hits++}else{misses++};t.Logf("FIND id=%s phrase=%q hit=%t error=%v",r.SessionID,phrase,found,err)
  time.Sleep(time.Second)
 }
 t.Logf("FIND_TOTAL sessions=%d system=%d hits=%d misses=%d empty=%d changed=%d",len(rows),system,hits,misses,empty,changed)
 // Independently inspect all user-message items, including ones after the display bound.
 w,_:=windowFor(url.Values{"days":{"7"}},7)
 var generators []struct{ID string `json:"id"`}
 err=c.query(context.Background(),`SELECT DISTINCT conversation_id AS id FROM cliproxy.request_facts WHERE `+timeFilter+` AND arrayExists((x,d,r,t)->d='input' AND r='user' AND t='message' AND startsWith(x,'Generate a title that will help the user recognize this T3 Code thread'),msg_text,msg_direction,msg_role,msg_type)`,w.params,&generators)
 if err!=nil {t.Fatal(err)}
 hidden:=0;for _,g:=range generators {for _,r:=range rows {if r.SessionID==g.ID&&r.System {hidden++}}};t.Logf("SYSTEM generators=%d hidden=%d",len(generators),hidden)
 if misses>0||empty>0||hidden!=len(generators) {t.Fail()}
}

var _ = fmt.Sprint
