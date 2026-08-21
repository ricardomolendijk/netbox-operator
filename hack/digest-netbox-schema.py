import json,sys
d=json.load(open(sys.argv[1]))
want=sys.argv[2].split(',') if len(sys.argv)>2 else None
for k,v in sorted(d.items()):
    if want and not any(k.endswith('.'+w) for w in want): continue
    if not v['fields']: continue
    print(f"## {k}   ({v['file']})")
    print(f"   bases: {', '.join(v['bases'])}")
    for f in v['fields']:
        req = '' if (f.get('null') or f.get('blank') or 'default' in f) else ' REQ'
        extra=[]
        if f.get('to'): extra.append(f"-> {f['to']}")
        if f.get('unique'): extra.append('UNIQUE')
        if f.get('max_length'): extra.append(f"len={f['max_length']}")
        if 'default' in f: extra.append(f"def={f['default']!r}")
        if f.get('choices'): extra.append(f"choices={f['choices']}")
        print(f"     {f['name']:28} {f['type']:22}{req:4} {' '.join(extra)}")
    for mk in ('unique_together','constraints','ordering','indexes'):
        if mk in v['meta']:
            print(f"   meta.{mk}: {v['meta'][mk][:400]}")
    print()
