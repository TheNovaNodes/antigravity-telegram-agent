import re
import ast
import json

def find_cyrillic_strings(filename):
    with open(filename, 'r', encoding='utf-8') as f:
        content = f.read()
    
    tree = ast.parse(content)
    cyrillic_strings = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Str):
            if re.search('[А-Яа-яЁё]', node.s):
                cyrillic_strings.add(node.s)
        elif isinstance(node, ast.JoinedStr):
            for value in node.values:
                if isinstance(value, ast.Constant) and isinstance(value.value, str):
                    if re.search('[А-Яа-яЁё]', value.value):
                        cyrillic_strings.add(value.value)
    
    for s in cyrillic_strings:
        print(repr(s))

find_cyrillic_strings('src/handlers.py')
