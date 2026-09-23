#!/usr/bin/env python3
import sys
import re
import os

tag = sys.argv[1] if len(sys.argv) > 1 else 'latest'
notes = ''
history_path = 'docs/CONVERSATION_HISTORY.md'

if os.path.exists(history_path):
    with open(history_path, 'r', encoding='utf-8') as f:
        content = f.read()
    # Try finding exact tag section: ## Turn \d+ - <tag> 发布记录
    pattern = rf'## Turn \d+ - {re.escape(tag)} 发布记录([\s\S]*?)(?=\n---\n\n## Turn|\Z)'
    m = re.search(pattern, content)
    if not m:
        # Fallback to latest turn if specific tag section is not found
        matches = list(re.finditer(r'## Turn \d+ - (v[0-9.]+) 发布记录([\s\S]*?)(?=\n---\n\n## Turn|\Z)', content))
        if matches:
            m = matches[-1]
    if m:
        notes = m.group(1).strip()

with open('release_notes.md', 'w', encoding='utf-8') as out:
    if notes:
        out.write(notes + '\n')
    else:
        out.write('版本更新与系统优化\n')
