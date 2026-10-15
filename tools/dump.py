import pypff, sys
def walk(f, depth, stats):
    name = f.name or ""
    n = f.number_of_sub_messages
    print("  "*depth + f"{name!r} msgs={n} sub={f.number_of_sub_folders}")
    for i in range(n):
        try:
            m = f.get_sub_message(i)
            subj = m.subject
            body = m.plain_text_body
            att = m.number_of_attachments
            rec = m.number_of_recipients if hasattr(m,'number_of_recipients') else '?'
            stats['msgs'] += 1
            for a in range(att):
                at = m.get_attachment(a)
                d = at.read_buffer(at.size) if at.size else b''
                stats['attbytes'] += len(d)
            print("  "*depth + f"   - {subj!r} body={len(body) if body else 0} att={att}")
        except Exception as e:
            stats['err'] += 1
            print("  "*depth + f"   ! message {i}: {e}")
    for i in range(f.number_of_sub_folders):
        walk(f.get_sub_folder(i), depth+1, stats)
p = pypff.file()
p.open(sys.argv[1])
stats = {'msgs':0,'err':0,'attbytes':0}
walk(p.get_root_folder(), 0, stats)
print("STATS", stats)
