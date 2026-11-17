import datetime
import json
import time

import attrs


def parse_dt(value):
    if isinstance(value, datetime.datetime):
        return value
    return datetime.datetime.fromisoformat(value)


@attrs.define
class LineItem:
    sku: str
    qty: int = attrs.field(validator=attrs.validators.ge(0))
    price: float = attrs.field(validator=attrs.validators.ge(0.0))

    def total(self):
        return self.qty * self.price


@attrs.define
class Order:
    id: int
    customer: str
    created: datetime.datetime = attrs.field(converter=parse_dt)
    items: list = attrs.field(converter=lambda value: [LineItem(**item) for item in value])
    tax_rate: float = 0.1

    def __attrs_post_init__(self):
        if not self.items:
            raise ValueError("order must contain at least one line item")

    def subtotal(self):
        return sum(item.total() for item in self.items)

    def total(self):
        return self.subtotal() * (1.0 + self.tax_rate)


def build_payloads():
    payloads = []
    for i in range(1000):
        if i % 10 == 0:
            items = []
        elif i % 7 == 0:
            items = [{"sku": "bad", "qty": -1, "price": 1.0}]
        elif i % 13 == 0:
            items = [{"sku": "expensive", "qty": 2, "price": 9.99}]
            created = "not-a-date"
            payloads.append({
                "id": i,
                "customer": "cust_" + str(i),
                "created": created,
                "items": items,
                "tax_rate": 0.08,
            })
            continue
        else:
            items = [
                {"sku": "a", "qty": 2, "price": 3.5},
                {"sku": "b", "qty": 1, "price": 10.0},
            ]
        payloads.append({
            "id": i,
            "customer": "cust_" + str(i),
            "created": "2025-01-01T00:00:00",
            "items": items,
            "tax_rate": 0.1,
        })
    return payloads


PAYLOADS = build_payloads()
RAW = json.dumps(PAYLOADS)


def process_batch(raw):
    data = json.loads(raw)
    orders = []
    errors = 0
    for record in data:
        try:
            order = Order(
                id=record["id"],
                customer=record["customer"],
                created=record["created"],
                items=record["items"],
                tax_rate=record.get("tax_rate", 0.1),
            )
            orders.append(order)
        except Exception:
            errors += 1
    summary = {
        "count": len(orders),
        "errors": errors,
        "revenue": round(sum(order.total() for order in orders), 2),
        "avg_items": (sum(len(order.items) for order in orders) / len(orders)) if orders else 0.0,
    }
    return json.dumps(summary, sort_keys=True)


def run_benchmark():
    # warmup
    process_batch(RAW)
    t0 = time.perf_counter()
    result = process_batch(RAW)
    elapsed = time.perf_counter() - t0
    return result, elapsed


if __name__ == "__main__":
    result, elapsed = run_benchmark()
    print(f"ETL Validation Benchmark completed in {elapsed * 1000:.2f} ms ({result})")