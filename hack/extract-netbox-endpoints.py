import re,os,sys
ROOT=sys.argv[1]
# Every `router.register(` must yield a row. A line this cannot parse used to be skipped in
# silence, and a missing endpoint row means a Kind never gets a CRD at all -- the one failure
# in this pipeline that is invisible in the output it produces. So: find the registrations
# first, parse them second, and stop the run on any that does not parse.
CALL=re.compile(r"router\.register\(")
# Either quote (the old pattern was single-quote only), any module prefix or none at all (it
# required a literal `views.`), and a name ending in ViewSet -- the model name is derived by
# stripping that suffix, so a viewset not named for its model is a wrong `app.Model` rather
# than a missing one, and has to be looked at rather than guessed at.
ROW=re.compile(r"""router\.register\(\s*(['"])(?P<ep>[^'"]+)\1\s*,\s*(?:\w+\.)*(?P<model>\w+)ViewSet\b""")
rows=[]
for app in ['circuits','core','dcim','extras','ipam','tenancy','users','virtualization','vpn','wireless']:
    u=os.path.join(ROOT,app,'api','urls.py')
    if not os.path.exists(u):
        # One missing file is every endpoint of that app missing at once.
        if os.path.isdir(os.path.join(ROOT,app)): print(f"!! {app}: no api/urls.py, no endpoints for that app", file=sys.stderr)
        continue
    src=open(u, encoding='utf-8').read()
    for c in CALL.finditer(src):
        m=ROW.match(src, c.start())
        if m is None:
            sys.exit(f"!! {u}:{src.count(chr(10),0,c.start())+1}: cannot parse "
                     f"{src[c.start():].splitlines()[0].strip()!r} -- the endpoint must be a string "
                     f"literal and the viewset a *ViewSet name, or the Kind gets no CRD at all")
        rows.append((f"{app}/{m.group('ep')}", f"{app}.{m.group('model')}"))
for a,b in rows: print(f"{a:52} {b}")
