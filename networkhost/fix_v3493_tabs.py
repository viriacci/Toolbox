from pathlib import Path
import sys
p = Path(sys.argv[1])
s = p.read_text(encoding='utf-8')
a = s.index('func (h *host) preloadTab')
b = s.index('func (h *host) handlers()', a)
block = s[a:b].replace('\\t', '\t')
s = s[:a] + block + s[b:]
p.write_text(s, encoding='utf-8')
print('normalized preload indentation')
