from pathlib import Path
from http.server import ThreadingHTTPServer, BaseHTTPRequestHandler
from playwright.sync_api import sync_playwright
import json, os, subprocess, threading, time, uuid, shutil
ROOT=Path(__file__).resolve().parent
STATE={}
LOCK=threading.Lock()
READY=threading.Event()
FINISHED=threading.Event()
BARRIER=None
PROCESSES=[]

def collect(label, proc, delay):
    with (ROOT/f'{delay}ms-{label}.log').open('w') as log:
        for line in proc.stdout:
            log.write(line); log.flush()
            if line.startswith('EVIDENCE '):
                e=json.loads(line[9:])
                with LOCK:
                    STATE[label].update(e)
                    if e['stage']=='commit':
                        STATE['events'].append(f"{label.upper():6} commit {e['transactions']:3}  {e['method']:22}  {e['elapsedMs']:8.2f} ms")
                    elif e['stage']=='result':
                        STATE['events'].append(f"{label.upper():6} READBACK VERIFIED: {e['replyBytes']} bytes; SHA-256 {e['replySHA256'][:20]}…")
                    if all(STATE[k].get('stage')=='ready' for k in ('before','after')):
                        STATE['phase']='ready'; READY.set()
        code=proc.wait()
        with LOCK:
            STATE[label]['exitCode']=code
            if code:
                STATE[label]['stage']='failed'; STATE['phase']='failed'; STATE['error']=f'{label} test exited {code}'; FINISHED.set(); READY.set()
            else:
                STATE[label]['stage']='passed'
                if all(STATE[k].get('stage')=='passed' for k in ('before','after')):
                    STATE['phase']='done'; FINISHED.set()

def prepare(delay):
    global STATE,BARRIER,PROCESSES
    READY.clear(); FINISHED.clear()
    BARRIER=ROOT/f'start-{uuid.uuid4().hex}'
    STATE={'delay':delay,'phase':'preparing','before':{},'after':{},'events':[]}
    PROCESSES=[]
    for label in ('before','after'):
        tmp=ROOT/f'tmp-{delay}-{label}'; tmp.mkdir(exist_ok=True)
        env=os.environ.copy(); env.update(AO_EVIDENCE_DELAY_MS=str(delay),AO_EVIDENCE_START_FILE=str(BARRIER),TMPDIR=str(tmp))
        proc=subprocess.Popen([str(ROOT/f'{label}.test'),'-test.run=^TestIssue5176Evidence$','-test.v','-test.timeout=60s'],stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True,bufsize=1,env=env,cwd=ROOT)
        PROCESSES.append(proc)
        threading.Thread(target=collect,args=(label,proc,delay),daemon=True).start()
    if not READY.wait(45): raise RuntimeError('Databases did not become ready')
    if STATE['phase']!='ready': raise RuntimeError(STATE)

class Handler(BaseHTTPRequestHandler):
    def log_message(self,*args): pass
    def do_GET(self):
        if self.path=='/state':
            with LOCK: data=json.dumps(STATE).encode()
            kind='application/json'
        else: data=(ROOT/'monitor.html').read_bytes(); kind='text/html'
        self.send_response(200);self.send_header('Content-Type',kind);self.send_header('Cache-Control','no-store');self.end_headers();self.wfile.write(data)
    def do_POST(self):
        if self.path!='/start': self.send_error(404); return
        with LOCK:
            if STATE['phase']!='ready': self.send_error(409); return
            STATE['phase']='running'; BARRIER.touch()
        self.send_response(204);self.end_headers()

server=ThreadingHTTPServer(('127.0.0.1',0),Handler)
threading.Thread(target=server.serve_forever,daemon=True).start()
url=f'http://127.0.0.1:{server.server_port}/'
print('Evidence monitor:',url,flush=True)
try:
    with sync_playwright() as p:
        for delay,name in [(0,'normal-storage'),(25,'controlled-latency')]:
            prepare(delay)
            context=p.chromium.launch_persistent_context(user_data_dir=str(ROOT/f'browser-{delay}'),headless=True,executable_path=os.environ.get('PLAYWRIGHT_CHROMIUM_EXECUTABLE'),viewport={'width':1280,'height':850},record_video_dir=str(ROOT/'videos'),record_video_size={'width':1280,'height':850})
            page=context.pages[0]
            page.goto(url)
            page.get_by_role('button',name='Replay 110 chunks').wait_for(state='visible')
            page.wait_for_timeout(1800) # Intentional reading time in the recording.
            page.get_by_role('button',name='Replay 110 chunks').click()
            page.wait_for_selector('body[data-done="true"], body[data-failed="true"]',timeout=45000)
            if page.locator('body').get_attribute('data-failed'): raise RuntimeError(page.locator('#error').inner_text())
            assert FINISHED.wait(5)
            with LOCK:
                result=json.loads(json.dumps(STATE))
            assert result['before']['transactions']==113,result
            assert result['after']['transactions']==4,result
            assert result['before']['replySHA256']==result['after']['replySHA256'],result
            (ROOT/f'{name}-results.json').write_text(json.dumps(result,indent=2)+'\n')
            page.screenshot(path=str(ROOT/f'{name}.png'))
            page.wait_for_timeout(7500) # Keep the measured result readable.
            video=page.video
            context.close()
            shutil.copyfile(video.path(),ROOT/f'{name}.webm')
            print(name, {k:{m:result[k].get(m) for m in ('transactions','archiveRows','cdcRows','rowChanges','elapsedMs','replySHA256')} for k in ('before','after')},flush=True)
finally:
    server.shutdown()
    for proc in PROCESSES:
        if proc.poll() is None: proc.terminate(); proc.wait(timeout=5)
