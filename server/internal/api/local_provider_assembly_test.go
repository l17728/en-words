package api

import (
 "context"
 "encoding/json"
 "net/http"
 "net/http/httptest"
 "testing"
 "enwords/internal/judge"
 "github.com/stretchr/testify/require"
)

func TestLocalProviderDiscoveryAndStructuredHealthGate(t *testing.T){
 for _,tc:=range []struct{name,discovery,completion string;ok bool}{
  {"success",`{"services":[{"id":"vl","model":"fixture","capabilities":["text"]}]}`,`{"ok":true}`,true},
  {"missing text",`{"services":[]}`,`{"ok":true}`,false},
  {"bad discovery",`bad json`,`{"ok":true}`,false},
  {"health false",`{"services":[{"id":"vl","model":"fixture","capabilities":["text"]}]}`,`{"ok":false}`,false},
  {"health malformed",`{"services":[{"id":"vl","model":"fixture","capabilities":["text"]}]}`,`not json`,false},
 }{t.Run(tc.name,func(t *testing.T){
  server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
   if r.URL.Path=="/api/models"{_,_=w.Write([]byte(tc.discovery));return}
   if r.URL.Path!="/api/vl/chat"{t.Errorf("unexpected path %s",r.URL.Path);w.WriteHeader(404);return}
   _=json.NewEncoder(w).Encode(map[string]any{"choices":[]any{map[string]any{"message":map[string]string{"content":tc.completion}}}})
  }));defer server.Close()
  t.Setenv("ENWORDS_MODEL_CONSOLE",server.URL)
  app,_:=newTestApp(t);err:=app.EnableLocalProviders(context.Background())
  if tc.ok{require.NoError(t,err);require.NotNil(t,app.Local);require.IsType(t,&judge.ZhipuLLM{},app.Judge.LLM)}else{require.Error(t,err);require.Nil(t,app.Local)}
 })}
 t.Run("invalid origin",func(t *testing.T){t.Setenv("ENWORDS_MODEL_CONSOLE","file:///fixture");a,_:=newTestApp(t);require.Error(t,a.EnableLocalProviders(context.Background()))})
}
