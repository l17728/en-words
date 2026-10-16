"""Authorized metadata-only deployment discovery. No inference or service edits."""
import base64
import json
from pathlib import Path

import paramiko
from probe_remote_gateway import ssh_arguments


REMOTE = r'''
import json,os,pathlib,re,subprocess,time
root=pathlib.Path('/root')
def safe_name(s):
 return '[redacted-name]' if re.search(r'(?i)(sk-[a-z0-9_-]{8,}|password=|token=|secret=)',s) else s
names=sorted(safe_name(p.name) for p in root.iterdir())
inodes=set()
for name in ('tcp','tcp6'):
 try:
  lines=pathlib.Path('/proc/net/'+name).read_text().splitlines()[1:]
  for line in lines:
   cols=line.split()
   if cols[1].rsplit(':',1)[1]=='1F41' and cols[3]=='0A': inodes.add(cols[9])
 except OSError: pass
rows=[]
for p in pathlib.Path('/proc').iterdir():
 if not p.name.isdigit(): continue
 matched=False
 try:
  for fd in (p/'fd').iterdir():
   try: target=os.readlink(fd)
   except OSError: continue
   if target.startswith('socket:[') and target[8:-1] in inodes: matched=True; break
 except OSError: continue
 if not matched: continue
 row={'pid':int(p.name)}
 for kind in ('exe','cwd'):
  try: row[kind]=safe_name(os.readlink(p/kind))
  except OSError: row[kind]='unavailable'
 units=[]
 try:
  for line in (p/'cgroup').read_text().splitlines():
   for component in line.split('/'):
    if re.fullmatch(r'[A-Za-z0-9_.@-]+\.service',component): units.append(component)
 except OSError: pass
 row['service_units']=sorted(set(units))
 paths=[]
 for unit in row['service_units']:
  try:
   done=subprocess.run(['systemctl','show',unit,'--property=FragmentPath','--value'],capture_output=True,text=True,timeout=3)
   value=done.stdout.strip()
   if done.returncode==0 and value.startswith('/') and '\n' not in value: paths.append(safe_name(value))
  except Exception: pass
 row['unit_file_paths']=paths
 # Version flag prints no launch arguments or configuration. Only nginx is allowed.
 if pathlib.Path(row.get('exe','')).name=='nginx':
  try:
   done=subprocess.run([row['exe'],'-v'],capture_output=True,text=True,timeout=3)
   match=re.search(r'nginx version: nginx/([0-9.]+)',done.stderr+done.stdout)
   row['nginx_version']=match.group(1) if match else 'unavailable'
  except Exception: row['nginx_version']='unavailable'
 rows.append(row)
print(json.dumps({'scope':'read_only_root_names_and_port8001_listener_identity','root_names':names[:200],'root_names_truncated':len(names)>200,'port8001_listener_inode_count':len(inodes),'listeners':sorted(rows,key=lambda r:r['pid']),'read_cmdline':False,'read_environ':False,'model_requests':0,'service_changes':False}))
'''


def main():
    output = Path('review/remote-gateway-deployment-discovery-20261016.json')
    if output.exists():
        raise ValueError('report_exists')
    client = paramiko.SSHClient()
    client.load_system_host_keys()
    client.set_missing_host_key_policy(paramiko.RejectPolicy())
    try:
        args, kwargs = ssh_arguments()
        kwargs.update(timeout=15, auth_timeout=15, banner_timeout=15, look_for_keys=False, allow_agent=False)
        client.connect(*args, **kwargs)
        encoded = base64.b64encode(REMOTE.encode()).decode()
        stdin, stdout, stderr = client.exec_command('python3 -c "import base64;exec(base64.b64decode(\'' + encoded + '\'))"', timeout=30)
        stdin.channel.shutdown_write()
        raw = stdout.read(32769)
        if len(raw) > 32768 or stdout.channel.recv_exit_status() != 0:
            raise ValueError('metadata_discovery_failed')
        report = json.loads(raw)
        with output.open('x', encoding='utf-8') as file:
            json.dump(report, file, ensure_ascii=False, indent=2)
        print(json.dumps(report, ensure_ascii=False))
    finally:
        client.close()


if __name__ == '__main__':
    try:
        main()
    except Exception as exc:
        print(json.dumps({'discovery_failed': type(exc).__name__}))
        raise SystemExit(1)
