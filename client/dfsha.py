#!/usr/bin/env python3
import argparse, hashlib, json, os, sys, urllib.error, urllib.parse, urllib.request
from concurrent.futures import ThreadPoolExecutor, as_completed

TOKEN_FILE = os.path.expanduser("~/.dfsha_token")

class DFSClient:
    def __init__(self, control):
        self.control = control.rstrip("/")
        self.token = os.environ.get("DFSHA_TOKEN") or self._read_token()

    def _read_token(self):
        try:
            return open(TOKEN_FILE, "r", encoding="utf-8").read().strip()
        except OSError:
            return ""

    def request(self, method, url, data=None, json_body=None, headers=None, timeout=120):
        h = dict(headers or {})
        if self.token:
            h.setdefault("Authorization", "Bearer " + self.token)
        if json_body is not None:
            data = json.dumps(json_body).encode()
            h["Content-Type"] = "application/json"
        req = urllib.request.Request(url, data=data, method=method, headers=h)
        try:
            with urllib.request.urlopen(req, timeout=timeout) as r:
                body = r.read()
                ctype = r.headers.get("Content-Type", "")
                if "application/json" in ctype:
                    return r.status, json.loads(body or b"{}")
                return r.status, body
        except urllib.error.HTTPError as e:
            body = e.read().decode(errors="replace")
            raise RuntimeError(f"HTTP {e.code}: {body}") from e

    def login(self, user, password):
        _, obj = self.request("POST", self.control + "/auth/login", json_body={"username":user,"password":password})
        self.token = obj["token"]
        with open(TOKEN_FILE, "w", encoding="utf-8") as f: f.write(self.token)
        print("Autenticación correcta; token guardado en", TOKEN_FILE)

    def mkdir(self, p):
        _, o = self.request("POST", self.control+"/fs/mkdir", json_body={"path":p}); print(json.dumps(o,indent=2))
    def ls(self, p):
        _, o = self.request("GET", self.control+"/fs/ls?"+urllib.parse.urlencode({"path":p})); print(json.dumps(o,indent=2))
    def stat(self, p):
        _, o = self.request("GET", self.control+"/fs/stat?"+urllib.parse.urlencode({"path":p})); print(json.dumps(o,indent=2))
    def rm(self, p):
        self.request("DELETE", self.control+"/fs/rm?"+urllib.parse.urlencode({"path":p})); print("Borrado de metadatos:",p)
    def rmdir(self, p):
        self.request("DELETE", self.control+"/fs/rmdir?"+urllib.parse.urlencode({"path":p})); print("Directorio borrado:",p)
    def mv(self, src, dst):
        _,o=self.request("POST",self.control+"/fs/mv",json_body={"src":src,"dst":dst});print(json.dumps(o,indent=2))

    def put(self, local, remote, workers=4):
        size=os.path.getsize(local)
        _,plan=self.request("POST",self.control+"/files/upload/plan",json_body={"path":remote,"size":size})
        bs=plan["block_size"]
        print(f"Plan: {plan['n_blocks']} bloques de hasta {bs} bytes")

        jobs=[]
        with open(local,"rb") as f:
            for bp in plan["blocks"]:
                data=f.read(bs)
                jobs.append((bp,data))

        def upload(job):
            bp,data=job
            block_id=hashlib.sha256(data).hexdigest()
            targets=bp["targets"]
            first=targets[0]
            url=f"http://{first['host']}:{first['port']}/blocks/{block_id}"
            headers={"Content-Type":"application/octet-stream"}
            if len(targets)>1:
                forwards=[]
                for i,t in enumerate(targets[1:], start=1):
                    forwards.append(f"http://{t['host']}:{t['port']}")
                    headers[f"X-Forward-Node-{i}"]=t["node"]
                headers["X-Forward-To"]=",".join(forwards)
            _,resp=self.request("PUT",url,data=data,headers=headers,timeout=300)
            stored=resp.get("stored_on",[t["node"] for t in targets])
            return {"index":bp["index"],"block_id":block_id,"size":len(data),"stored_on":stored}

        results=[]
        try:
            with ThreadPoolExecutor(max_workers=workers) as ex:
                futs=[ex.submit(upload,j) for j in jobs]
                for fut in as_completed(futs):
                    b=fut.result();results.append(b);print(f"  bloque {b['index']} -> {', '.join(b['stored_on'])}")
        except Exception:
            try:self.request("POST",self.control+"/files/upload/abort",json_body={"upload_id":plan["upload_id"]})
            finally:raise
        results.sort(key=lambda x:x["index"])
        _,commit=self.request("POST",self.control+"/files/upload/commit",json_body={"upload_id":plan["upload_id"],"blocks":results})
        print(json.dumps(commit,indent=2))

    def get(self, remote, local, workers=4):
        _,plan=self.request("GET",self.control+"/files/download/plan?"+urllib.parse.urlencode({"path":remote}))
        def fetch(b):
            last=None
            for r in b["replicas"]:
                url=f"http://{r['host']}:{r['port']}/blocks/{b['block_id']}"
                try:
                    _,data=self.request("GET",url,timeout=300)
                    if hashlib.sha256(data).hexdigest()!=b["block_id"]: raise RuntimeError("hash inválido")
                    return b["index"],data,r["node"]
                except Exception as e:last=e
            raise last or RuntimeError("sin réplicas")
        chunks={}
        with ThreadPoolExecutor(max_workers=workers) as ex:
            futs=[ex.submit(fetch,b) for b in plan["blocks"]]
            for fut in as_completed(futs):
                idx,data,node=fut.result();chunks[idx]=data;print(f"  bloque {idx} <- {node}")
        with open(local,"wb") as f:
            for i in range(len(plan["blocks"])):f.write(chunks[i])
        print(f"Archivo reconstruido en {local} ({os.path.getsize(local)} bytes)")

def main():
    p=argparse.ArgumentParser(description="Cliente CLI de DFSha Hito 2")
    p.add_argument("--control",default=os.environ.get("DFSHA_CONTROL","http://localhost:8000"))
    sub=p.add_subparsers(dest="cmd",required=True)
    a=sub.add_parser("login");a.add_argument("username");a.add_argument("password")
    a=sub.add_parser("ls");a.add_argument("path",nargs="?",default="/")
    a=sub.add_parser("mkdir");a.add_argument("path")
    a=sub.add_parser("stat");a.add_argument("path")
    a=sub.add_parser("rm");a.add_argument("path")
    a=sub.add_parser("rmdir");a.add_argument("path")
    a=sub.add_parser("mv");a.add_argument("src");a.add_argument("dst")
    a=sub.add_parser("put");a.add_argument("local");a.add_argument("remote");a.add_argument("--workers",type=int,default=4)
    a=sub.add_parser("get");a.add_argument("remote");a.add_argument("local");a.add_argument("--workers",type=int,default=4)
    args=p.parse_args();c=DFSClient(args.control)
    if args.cmd=="login":c.login(args.username,args.password)
    elif not c.token:sys.exit("Primero ejecuta: dfsha.py login demo demo")
    elif args.cmd=="ls":c.ls(args.path)
    elif args.cmd=="mkdir":c.mkdir(args.path)
    elif args.cmd=="stat":c.stat(args.path)
    elif args.cmd=="rm":c.rm(args.path)
    elif args.cmd=="rmdir":c.rmdir(args.path)
    elif args.cmd=="mv":c.mv(args.src,args.dst)
    elif args.cmd=="put":c.put(args.local,args.remote,args.workers)
    elif args.cmd=="get":c.get(args.remote,args.local,args.workers)
if __name__=="__main__":main()
