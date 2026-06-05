import json, collections, re
P="/home/cowboy/recon/02-vector-databases-2026-06-04-virgin/scan-dedicated.jsonl"
rows=[]
for l in open(P, errors="replace"):
    l=l.strip()
    if not l: continue
    try: rows.append(json.loads(l))
    except: pass
print(f"total banner hits: {len(rows)}  unique IPs: {len({r['ip'] for r in rows})}")

byport=collections.Counter(r['port'] for r in rows)
print("\n=== hits by port ===")
for p,c in sorted(byport.items(), key=lambda x:-x[1]):
    print(f"  {p:6} {c}")

def cls(r):
    b=r.get('banner','').lower()
    if 'qdrant - vector search engine' in b: return 'qdrant'
    if 'nanosecond heartbeat' in b: return 'chroma'
    if '"modules"' in b and 'hostname' in b: return 'weaviate'
    if 'welcome to marqo' in b: return 'marqo'
    if 'com.yahoo.vespa' in b: return 'vespa'
    if 'redisinsight' in b: return 'redisinsight'
    if 'redis_version' in b or b.startswith('+pong'): return 'redis'
    if 'milvus' in b: return 'milvus'
    if 'surreal' in b: return 'surreal'
    if 'databend' in b: return 'databend'
    if r.get('protocol')=='TCP': return 'binary/grpc'
    if 'nginx' in b: return 'nginx-FP'
    if 'cloudflare' in b or 'cf-ray' in b: return 'cloudflare-FP'
    if '401' in b[:40] or 'unauthorized' in b[:80]: return 'auth-401'
    if '200 ok' in b[:30]: return 'http-200-other'
    if any(x in b for x in ['400 bad request','403 ','404 ','302 ','301 ']): return 'http-other'
    return 'other'

kinds=collections.Counter(cls(r) for r in rows)
print("\n=== banner classification ===")
for k,c in kinds.most_common(): print(f"  {k:18} {c}")

qv=collections.Counter()
for r in rows:
    b=r.get('banner','')
    if 'qdrant - vector search engine' in b.lower():
        m=re.search(r'"version":"([^"]+)"', b)
        if m: qv[m.group(1)]+=1
print("\n=== Qdrant versions (live) ===")
for v,c in qv.most_common(12): print(f"  {v:12} {c}")

shadow={6379:'redis',5432:'postgres',8123:'clickhouse',9000:'minio/ch',9001:'minio-console',
        2379:'etcd',9090:'prometheus',3000:'attu/grafana',5000:'docker-reg',55000:'docker-reg',
        27017:'mongo',8888:'jupyter/epsilla'}
print("\n=== shadow-port surface on vector-DB hosts ===")
sp=collections.Counter(r['port'] for r in rows if r['port'] in shadow)
for p,c in sorted(sp.items(),key=lambda x:-x[1]): print(f"  {p:6} {shadow[p]:16} {c}")
