// One outstanding batch; at most two seconds buffered when the UI thread stalls.
export const workletSource = `class Capture extends AudioWorkletProcessor {
  constructor() { super(); this.pending=[]; this.inflight=false; this.failed=false;
    this.port.onmessage=()=>{this.inflight=false;this.flush()}; }
  flush() {
    if(this.inflight)return;
    if(this.failed){this.port.postMessage({overflow:true});this.inflight=true;return;}
    const size=Math.round(sampleRate*0.08);
    if(this.pending.length>=size){const batch=Float32Array.from(this.pending.splice(0,size));this.inflight=true;this.port.postMessage(batch,[batch.buffer]);}
  }
  process(inputs) {
    const channels=inputs[0];
    if(!this.failed && channels?.length){
      if(this.pending.length+channels[0].length>sampleRate*2){this.failed=true;this.pending=[];}
      else for(let i=0;i<channels[0].length;i++){let value=0;for(const c of channels)value+=c[i]/channels.length;this.pending.push(value);}
    }
    this.flush();return true;
  }
} registerProcessor('duplex-preflight',Capture);`
