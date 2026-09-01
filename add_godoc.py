import re

def add_godoc(filepath):
    with open(filepath, 'r') as f:
        lines = f.readlines()

    out_lines = []
    
    type_pattern = re.compile(r'^type ([A-Z][a-zA-Z0-9_]*) struct')
    func_pattern = re.compile(r'^func ([A-Z][a-zA-Z0-9_]*)\(')
    meth_pattern = re.compile(r'^func \([^)]+\) ([A-Z][a-zA-Z0-9_]*)\(')
    
    for i, line in enumerate(lines):
        tm = type_pattern.match(line)
        if tm:
            name = tm.group(1)
            # Check if previous line is a comment
            if i > 0 and not lines[i-1].strip().startswith('//'):
                out_lines.append(f'// {name} represents the {name} data structure.\n')
        
        fm = func_pattern.match(line)
        if fm:
            name = fm.group(1)
            if i > 0 and not lines[i-1].strip().startswith('//'):
                out_lines.append(f'// {name} executes the {name} operation.\n')

        mm = meth_pattern.match(line)
        if mm:
            name = mm.group(1)
            if i > 0 and not lines[i-1].strip().startswith('//'):
                out_lines.append(f'// {name} performs the {name} method.\n')
                
        out_lines.append(line)
        
    with open(filepath, 'w') as f:
        f.writelines(out_lines)

add_godoc("agysessionsstarter.go")
