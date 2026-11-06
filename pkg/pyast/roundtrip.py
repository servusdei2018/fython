import ast, sys
bad = 0
for pair in sys.argv[1:]:
    orig, new = pair.split("::")
    try:
        a = ast.dump(ast.parse(open(orig, "rb").read()))
    except SyntaxError:
        continue
    try:
        b = ast.dump(ast.parse(open(new, "rb").read()))
    except SyntaxError as e:
        print("SYNTAX", orig, e)
        continue
    if a != b:
        # find first difference
        i = 0
        while i < min(len(a), len(b)) and a[i] == b[i]:
            i += 1
        print("DIFF", orig, "...", a[max(0,i-80):i+80].replace("\n"," "), "|||", b[max(0,i-80):i+80].replace("\n"," "))
