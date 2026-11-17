import io
import time

from pygments import lex
from pygments.lexers import PythonLexer
from rich.console import Console
from rich.syntax import Syntax
from rich.table import Table

SOURCE = "\n".join(
    "def func_{i}(x, y):\n"
    "    \"\"\"Compute a value for iteration {i}.\"\"\"\n"
    "    result = (x * {i}) + (y // ({i} + 1))\n"
    "    if result % 2 == 0:\n"
    "        return result\n"
    "    return result - 1\n".format(i=i)
    for i in range(50)
)


def render():
    tokens = list(lex(SOURCE, PythonLexer()))
    buf = io.StringIO()
    console = Console(file=buf, force_terminal=True, width=120, color_system="truecolor")
    syntax = Syntax(SOURCE, "python", theme="monokai", line_numbers=True)
    console.print(syntax)
    table = Table(title="Lexing Results")
    table.add_column("Index")
    table.add_column("Token Type")
    table.add_column("Value")
    for idx, (tok_type, value) in enumerate(tokens[:60]):
        table.add_row(str(idx), str(tok_type), repr(value))
    console.print(table)
    return len(buf.getvalue()) + len(tokens)


def run_benchmark():
    # warmup
    render()
    t0 = time.perf_counter()
    out = render()
    elapsed = time.perf_counter() - t0
    return out, elapsed


if __name__ == "__main__":
    out, elapsed = run_benchmark()
    print(f"Rich/Pygments Lexing Benchmark completed in {elapsed * 1000:.2f} ms ({out} units)")