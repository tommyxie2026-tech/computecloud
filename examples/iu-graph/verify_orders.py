#!/usr/bin/env python3
"""Fixed, read-only example verifier. Input: iu-context-v1 on stdin."""
import json
import sys

VERSION = "iu-orders-verifier-1"

def integer(value):
    return type(value) is int and 0 <= value <= 10**12

def verify(context):
    unknown = {"result": "UNKNOWN", "expected_total": None}
    try:
        rules = [json.loads(item["text"]) for item in context["rules"]]
        if len(rules) != 1:
            return unknown
        threshold, discount = rules[0]["threshold_cents"], rules[0]["discount_cents"]
        if not integer(threshold) or not integer(discount):
            return unknown
        orders = {}
        for item in context["local"]:
            order = json.loads(item["text"])
            if (not isinstance(order["order_id"], str) or not order["order_id"]
                    or not isinstance(order["customer_id"], str) or not order["customer_id"]
                    or not integer(order["paid_cents"])):
                return unknown
            identity = order["order_id"]
            normalized = (order["customer_id"], order["paid_cents"])
            if identity in orders and orders[identity] != normalized:
                return unknown
            orders[identity] = normalized
        if not orders:
            return unknown
        totals = {}
        for customer, amount in orders.values():
            totals[customer] = totals.get(customer, 0) + amount
        expected = sum(max(0, amount - discount) if amount >= threshold else amount
                       for amount in totals.values())
        candidates = [json.loads(item["value"]) for item in context["inputs"] if item["port"] == "amount"]
        if len(candidates) != 1 or not integer(candidates[0]):
            return unknown
        return {"result": "PASS" if candidates[0] == expected else "FAIL", "expected_total": expected}
    except (KeyError, TypeError, ValueError, AttributeError):
        return unknown

def main():
    if sys.argv[1:] == ["--version"]:
        print(VERSION)
        return
    # Context size is bounded by the graph protocol; reject oversize input independently.
    raw = sys.stdin.buffer.read(65537)
    try:
        context = json.loads(raw) if len(raw) <= 65536 else {}
    except (ValueError, UnicodeError):
        context = {}
    print(json.dumps({"version": "iu-node-output-v1", "values": verify(context), "records": []},
                     separators=(",", ":")))

if __name__ == "__main__":
    main()
