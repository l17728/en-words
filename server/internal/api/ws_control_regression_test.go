package api

import (
 "net/http"
 "net/http/httptest"
 "runtime"
 "strings"
 "testing"
 "time"
 "github.com/gorilla/websocket"
 "github.com/stretchr/testify/require"
)

func TestWSHeartbeatCapacityAndClosedSend(t *testing.T) {
 c:=&wsClient{send:make(chan Msg,64),control:make(chan Msg,1),done:make(chan struct{})}
 for i:=0;i<64;i++{require.NoError(t,c.Send(Msg{"type":"fixture"}))}
 require.ErrorIs(t,c.Send(Msg{"type":"overflow"}),errBackpressure)
 for i:=0;i<100;i++{require.NoError(t,c.Send(Msg{"type":"heartbeat_ack"}))}
 require.Len(t,c.control,1,"redundant unsequenced ACKs coalesce")
 require.Len(t,c.send,64,"heartbeat never displaces ordinary queued messages")
 require.NoError(t,c.Close())
 for i:=0;i<100;i++{
  require.ErrorIs(t,c.Send(Msg{"type":"heartbeat_ack"}),errClosed)
  require.ErrorIs(t,c.Send(Msg{"type":"fixture"}),errClosed)
 }
 require.NoError(t,c.Close())
}

func TestWSOrdinaryFramesProgressDuringContinuousHeartbeats(t *testing.T) {
 clients:=make(chan *wsClient,1)
 srv:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  conn,err:=upgrader.Upgrade(w,r,nil);if err!=nil{return}
  c:=newWSClient(conn);clients<-c
  <-c.done
 }))
 defer srv.Close()
 conn,_,err:=websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL,"http"),nil)
 require.NoError(t,err);defer conn.Close()
 c:=<-clients;defer c.Close()
 stop:=make(chan struct{});stopped:=make(chan struct{})
 go func(){defer close(stopped);for{select{case <-stop:return;default: _=c.Send(Msg{"type":"heartbeat_ack"});runtime.Gosched()}}}()
 defer func(){close(stop);<-stopped}()
 for i:=0;i<32;i++{require.NoError(t,c.Send(Msg{"type":"ordinary","index":i}))}
 require.NoError(t,conn.SetReadDeadline(time.Now().Add(5*time.Second)))
 received:=0
 for received<32{
  var message Msg;require.NoError(t,conn.ReadJSON(&message))
  if message["type"]=="ordinary"{require.EqualValues(t,received,message["index"]);received++}
 }
}
