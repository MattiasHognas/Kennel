import re, os
for f in ['editor.go', 'activity.go', 'lifecycle.go']:
    path = os.path.join('internal/logic', f)
    with open(path, 'r', encoding='utf-8') as file:
        text = file.read()
    # remove all import blocks
    text = re.sub(r'import\s+\([^)]+\)', '', text)
    # remove single line imports
    text = re.sub(r'import\s+".*?"', '', text)
    with open(path, 'w', encoding='utf-8') as file:
        file.write(text)
