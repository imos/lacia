#!/usr/bin/env python3
"""Refresh the embedded Cabinet Office holiday data, without guessing dates."""
import csv
import datetime
import io
import pathlib
import urllib.request

URL = "https://www8.cao.go.jp/chosei/shukujitsu/syukujitsu.csv"
with urllib.request.urlopen(URL, timeout=30) as response:
    text = response.read(1024 * 1024 + 1)
if len(text) > 1024 * 1024:
    raise ValueError("祝日のCSVが1 MiBを超えています。")
rows = list(csv.reader(io.StringIO(text.decode("cp932"))))
if not rows or rows.pop(0) != ["国民の祝日・休日月日", "国民の祝日・休日名称"]:
    raise ValueError("祝日のCSVの見出しが、想定した形式と違います。")
output = io.StringIO()
writer = csv.writer(output, lineterminator="\n")
writer.writerow(["date", "name"])
seen = set()
for date, name in rows:
    year, month, day = map(int, date.split("/"))
    date = datetime.date(year, month, day).isoformat()
    if date in seen or not name.strip():
        raise ValueError("祝日のCSVに、重複した日付か、名前が空の行があります。")
    seen.add(date)
    writer.writerow([date, name])
if not seen:
    raise ValueError("祝日のCSVに、日付がありません。")
path = pathlib.Path(__file__).resolve().parents[1] / "internal/calendar/holidays.csv"
path.write_text(output.getvalue(), encoding="utf-8")
print(f"{min(seen)} から {max(seen)} までの祝日を{len(rows)}件保存しました。")
