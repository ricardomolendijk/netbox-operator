import re,os,sys,glob
ROOT=sys.argv[1]
rows=[]
for app in ['circuits','core','dcim','extras','ipam','tenancy','users','virtualization','vpn','wireless']:
    u=os.path.join(ROOT,app,'api','urls.py')
    if not os.path.exists(u): continue
    for m in re.finditer(r"router\.register\('([^']+)',\s*views\.(\w+)", open(u).read()):
        ep, vs = m.group(1), m.group(2)
        model = vs.replace('ViewSet','')
        rows.append((f"{app}/{ep}", f"{app}.{model}"))
for a,b in rows: print(f"{a:52} {b}")
