import pypff, sys, hashlib
SKIP = {0x0E08, 0x67F3, 0x0E30, 0x0E33, 0x0E34, 0x67F4}  # size, row ver, repl tags (expected to change)
def entries(rs):
    out = {}
    for e in rs.entries:
        t = e.entry_type
        if t in SKIP: continue
        try: d = e.data or b''
        except Exception: d = b'?'
        out[(t, e.value_type)] = hashlib.md5(d).hexdigest()[:8]
    return out
def msg_sig(m):
    sig = {'props': entries(m.get_record_set(0)) if m.number_of_record_sets else {}}
    sig['rcpt'] = []
    try:
        r = getattr(m, "recipients", None)
        for i in range(r.number_of_record_sets if r else 0):
            sig['rcpt'].append(entries(r.get_record_set(i)))
    except Exception as e: sig['rcpt'] = 'ERR %s' % e
    atts = []
    for a in range(getattr(m, "number_of_attachments", 0)):
        at = m.get_attachment(a)
        d = at.read_buffer(at.size) if at.size else b''
        atts.append((at.size, hashlib.md5(d).hexdigest()[:8]))
    sig['att'] = atts
    return sig
def collect(f, path, out):
    for i in range(f.number_of_sub_messages):
        m = f.get_sub_message(i)
        out[(path, getattr(m,"subject",None), m.identifier if False else i)] = msg_sig(m)
    for i in range(f.number_of_sub_folders):
        s = f.get_sub_folder(i)
        collect(s, path + '/' + (s.name or ''), out)
def load(fn, start):
    p = pypff.file(); p.open(fn)
    f = p.get_root_folder()
    for name in start:
        f = [f.get_sub_folder(i) for i in range(f.number_of_sub_folders) if f.get_sub_folder(i).name == name][0]
    out = {}; collect(f, '', out); return out
a = load(sys.argv[1], [x for x in sys.argv[2].split('/') if x])
b = load(sys.argv[3], [x for x in sys.argv[4].split('/') if x])
print('messages:', len(a), 'vs', len(b))
bad = 0
for k in a:
    if k not in b: print('MISSING', k); bad += 1; continue
    if a[k] != b[k]:
        bad += 1
        pa, pb = a[k]['props'], b[k]['props']
        print('DIFF', k)
        for kk in sorted(set(pa) | set(pb)):
            if pa.get(kk) != pb.get(kk): print('   prop %04x/%04x: %s -> %s' % (kk[0], kk[1], pa.get(kk), pb.get(kk)))
        if a[k]['rcpt'] != b[k]['rcpt']: print('   recipients differ', a[k]['rcpt'], b[k]['rcpt'])
        if a[k]['att'] != b[k]['att']: print('   attachments differ', a[k]['att'], b[k]['att'])
print('RESULT', 'OK' if bad == 0 else '%d differences' % bad)
