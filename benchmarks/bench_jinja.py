import io
import time

import jinja2

TEMPLATE = """{% macro row(r) -%}
<tr class="{{ 'active' if r.active else 'inactive' }}">
  <td>{{ r.id }}</td>
  <td>{{ r.name|e }}</td>
  <td>{{ r.score }}</td>
  <td>{% if r.score >= threshold %}PASS{% else %}FAIL{% endif %}</td>
</tr>
{%- endmacro %}
<!DOCTYPE html>
<html>
  <head><title>{{ title }}</title></head>
  <body>
    <h1>{{ title }}</h1>
    <table>
      <thead><tr><th>ID</th><th>Name</th><th>Score</th><th>Status</th></tr></thead>
      <tbody>
      {% for r in rows %}
        {{ row(r) }}
      {% endfor %}
      </tbody>
    </table>
    <p>Total rows: {{ rows|length }}</p>
  </body>
</html>
"""


def build_context():
    return {
        "title": "Benchmark Report",
        "threshold": 60,
        "rows": [
            {"id": i, "name": "user_" + str(i), "score": (i * 7) % 100, "active": (i % 2 == 0)}
            for i in range(50)
        ],
    }


def render_page(env, context):
    template = env.from_string(TEMPLATE)
    buf = io.StringIO()
    for _ in range(200):
        buf.write(template.render(**context))
    return len(buf.getvalue())


def run_benchmark():
    env = jinja2.Environment(autoescape=True)
    context = build_context()
    # warmup
    render_page(env, context)
    t0 = time.perf_counter()
    out_len = render_page(env, context)
    elapsed = time.perf_counter() - t0
    return out_len, elapsed


if __name__ == "__main__":
    out_len, elapsed = run_benchmark()
    print(f"Jinja2 Template Benchmark completed in {elapsed * 1000:.2f} ms ({out_len} bytes)")