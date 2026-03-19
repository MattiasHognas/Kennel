import re

with open('internal/logic/model.go', 'r', encoding='utf-8') as f:
    lines = f.readlines()

def get_block(start_regex):
    start_idx = -1
    for i, line in enumerate(lines):
        if re.search(start_regex, line):
            start_idx = i
            break
    if start_idx == -1: return ""
    
    # if it's a func or type, it ends when opening braces match closing braces, OR simple line breaks for single lines.
    # We'll do brace counting
    brace_count = 0
    in_block = False
    end_idx = start_idx
    for i in range(start_idx, len(lines)):
        line = lines[i]
        brace_count += line.count('{') - line.count('}')
        if brace_count > 0:
            in_block = True
        if in_block and brace_count == 0:
            end_idx = i
            break
        if not in_block and (line.startswith('type ') or line.startswith('func ') or line.startswith('const ') or line.startswith('var ')):
            # single line maybe?
            if line.strip().endswith('}'):
                end_idx = i
                break
            if '{' not in line and '(' not in line.split()[-1]: # single line type mapping maybe? 
                pass
    
    # For `type Name struct { ... }` or `const ( ... )`
    paren_count = 0
    in_paren = False
    if 'const ' in lines[start_idx] or 'var ' in lines[start_idx]:
        for i in range(start_idx, len(lines)):
            line = lines[i]
            paren_count += line.count('(') - line.count(')')
            if paren_count > 0:
                in_paren = True
            if in_paren and paren_count == 0:
                end_idx = i
                break
            if not in_paren and brace_count == 0:
                if "{ " not in line and not line.strip().endswith("{"):
                    end_idx = i
                    break
                    
    content = "".join(lines[start_idx:end_idx+1])
    # blank out the lines so we don't grab them again
    for i in range(start_idx, end_idx+1):
        lines[i] = ""
    return content

editor_funcs = [
    r'^type projectEditor struct',
    r'^func newProjectEditor\(',
    r'^func \(m \*Model\) openSelectedProjectEditor',
    r'^func \(m \*Model\) closeSelectedProjectEditor',
    r'^func \(m \*Model\) setProjectEditorFocus',
    r'^func \(m Model\) projectEditorView',
    r'^func \(m Model\) projectEditorOKButtonView',
    r'^func \(m Model\) projectEditorOKButtonBounds',
    r'^func \(m \*Model\) updateProjectEditor',
    r'^func \(m \*Model\) saveSelectedProjectEditor',
    r'^func \(m \*Model\) resizeProjectEditor',
    r'^func \(m \*Model\) refreshProjectAndSelection'
]

lifecycle_funcs = [
    r'^func \(m \*Model\) startSelectedProject',
    r'^func \(m \*Model\) stopSelectedProject',
    r'^func \(m \*Model\) completeSelectedProject',
    r'^func \(m \*Model\) startSelectedAgent',
    r'^func \(m \*Model\) stopSelectedAgent',
    r'^func \(m \*Model\) completeSelectedAgent',
    r'^func \(m \*Model\) cycleSelectedProjectState',
    r'^func \(m \*Model\) cycleSelectedAgentState'
]

activity_funcs = [
    r'^func \(m \*Model\) recordActivity',
    r'^func \(m \*Model\) BuildActivitySources',
    r'^func waitForActivity'
]

editor_content = "\n".join([get_block(r) for r in editor_funcs])
lifecycle_content = "\n".join([get_block(r) for r in lifecycle_funcs])
activity_content = "\n".join([get_block(r) for r in activity_funcs])

model_content = "".join(lines) # Whatever is left

# Remove the original imports from model_content
model_content = re.sub(r'(?s)import \(\n.*?\n\)', '', model_content, count=1)

with open('internal/logic/editor.go', 'w', encoding='utf-8') as f:
    f.write('package model\n\n' + editor_content)

with open('internal/logic/lifecycle.go', 'w', encoding='utf-8') as f:
    f.write('package model\n\n' + lifecycle_content)

with open('internal/logic/activity.go', 'w', encoding='utf-8') as f:
    f.write('package model\n\n' + activity_content)

with open('internal/logic/model.go', 'w', encoding='utf-8') as f:
    f.write(model_content)

print("Done")
