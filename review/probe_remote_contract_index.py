"""Read only top-level Markdown names and relevant protocol field presence."""
import json
import re
from pathlib import Path
import paramiko
from probe_remote_gateway import ssh_arguments

def main():
    destination=Path('review/remote-gateway-contract-index-20261015.json')
    if destination.exists(): raise ValueError('report_exists')
    client=paramiko.SSHClient(); client.load_system_host_keys(); client.set_missing_host_key_policy(paramiko.RejectPolicy())
    try:
        args,kwargs=ssh_arguments(); kwargs.update(timeout=15,auth_timeout=15,banner_timeout=15,look_for_keys=False,allow_agent=False)
        client.connect(*args,**kwargs)
        with client.open_sftp() as sftp:
            names=sorted(x for x in sftp.listdir('/root') if x.lower().endswith('.md'))
            selected=[x for x in names if any(term in x.lower() for term in ('duplex','llm','audio','omni','gateway','minicpm'))]
            documents=[]
            for name in selected:
                with sftp.open('/root/'+name) as file: raw=file.read(262145)
                if len(raw)>262144: documents.append({'name':name,'error':'size_limit'}); continue
                text=raw.decode('utf-8')
                fields=('input_audio','audio_url','pcm16','sample_rate','response.cancel','input_audio_buffer.append','response.audio.delta','transcript.final','session.update','websocket','realtime')
                documents.append({'name':name,'keyword_presence_only':{field:field in text.lower() for field in fields},'versioned_implementation_contract_verified':False})
        report={'scope':'top_level_markdown_names_and_relevant_keyword_presence_only','markdown_names':names,'relevant_documents':documents,'audio_capability':'unknown','required_evidence':['authenticated_transport_and_endpoint','model_revision','audio_chunk_format_and_sample_rate','stream_order_and_backpressure','cancel_ack_and_final_events','real_protocol_example']}
        with destination.open('x',encoding='utf-8') as f: json.dump(report,f,ensure_ascii=False,indent=2)
        print(json.dumps(report,ensure_ascii=False))
    finally: client.close()

if __name__=='__main__':
    try: main()
    except Exception as error:
        print(json.dumps({'probe_failed':type(error).__name__})); raise SystemExit(1)
