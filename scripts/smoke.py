#!/usr/bin/env python3
"""Integration smoke test against a running server and PostgreSQL.

Run with DATABASE_URL, ADMIN_KEY and PORT set. Uses two separate cookie jars.
"""
import http.cookiejar
import os
import time
import urllib.error
import urllib.parse
import urllib.request
import re

BASE = "http://127.0.0.1:" + os.environ.get("PORT", "8080")
ADMIN = os.environ["ADMIN_KEY"]


def client():
    jar = http.cookiejar.CookieJar()
    return urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))


def request(opener, path, fields=None, headers=None):
    data = urllib.parse.urlencode(fields).encode() if fields is not None else None
    req = urllib.request.Request(BASE + path, data=data, headers=headers or {})
    with opener.open(req, timeout=15) as response:
        return response.status, response.url, response.read().decode("utf-8")


def expect_forbidden(opener, path):
    try:
        request(opener, path)
    except urllib.error.HTTPError as exc:
        assert exc.code in (401, 403), (path, exc.code)
    else:
        raise AssertionError("Expected denied access: " + path)


def run():
    admin = client()
    for _ in range(90):
        try:
            status, _, _ = request(admin, "/")
            if status == 200:
                break
        except (urllib.error.URLError, TimeoutError):
            time.sleep(1)
    else:
        raise RuntimeError("Server failed to start")

    expect_forbidden(admin, "/admin")
    expect_forbidden(admin, "/admin/export")
    expect_forbidden(admin, "/api/leaderboard")

    assigned_groups = []
    for number in (1, 2):
        person = client()
        status, _, landing = request(person, "/")
        assert status == 200 and "name=\"name\"" in landing and "baseline_stress" not in landing
        status, url, briefing = request(person, "/start", {
            "name": "Pilot " + str(number),
            "consent": "yes",
        })
        assert status == 200 and "/briefing" in url, url
        assert "Начать отвечать" in briefing and "baseline_stress" in briefing
        is_ranking = "Соревновательный режим" in briefing
        assigned_groups.append(is_ranking)
        if number == 2:
            assert assigned_groups[0] != assigned_groups[1], "Balanced assignment mismatch"

        status, url, blocked = request(person, "/test")
        assert "/briefing" in url and "answer_q" not in blocked

        status, url, page = request(person, "/begin", {"baseline_stress": "3"})
        assert "/test" in url and "quizForm" in page
        assert "Таблица лидеров" not in page
        assert "Соревновательный режим" not in page

        # Question IDs and option indices are read from the quiz HTML.
        names = re.findall(r'name="answer_([^"]+)" value="0"', page)
        assert names, "No quiz questions"
        answers = {"answer_" + name: "0" for name in names}
        answers.update({"time_" + name: "250" for name in names})
        answers["stress"] = "4"
        status, url, result = request(person, "/finish", answers)
        assert status == 200 and "/result" in url
        assert "Тест завершён" in result
        assert ("Таблица лидеров" in result) == is_ranking

        status, url, _ = request(person, "/finish", answers)
        assert "/result" in url, "Second submission should redirect to result"
        if is_ranking:
            status, _, ranking = request(person, "/api/leaderboard")
            assert "Pilot " + str(number) in ranking
        else:
            expect_forbidden(person, "/api/leaderboard")

    # Legacy forced-group links must redirect to the single randomized entry.
    for group in ("ranking", "control"):
        status, url, landing = request(client(), "/join/" + group)
        assert status == 200 and url.rstrip("/") == BASE
        assert 'action="/start"' in landing

    # 60 registrations must produce exactly 30 assignments per condition.
    # The first two users above already occupy one place in each group.
    totals = {True: 1, False: 1}
    for number in range(3, 61):
        person = client()
        status, url, briefing = request(person, "/start", {
            "name": "Pilot " + str(number),
            "consent": "yes",
        })
        assert "/briefing" in url
        totals["Соревновательный режим" in briefing] += 1
    assert totals == {True: 30, False: 30}, totals

    headers = {"X-Admin-Key": ADMIN}
    status, _, dashboard = request(admin, "/admin", headers=headers)
    assert status == 200 and "Pilot 1" in dashboard and "Pilot 2" in dashboard
    assert "Сравнение групп" in dashboard
    status, _, csv = request(admin, "/admin/export", headers=headers)
    assert status == 200 and "test_started_at" in csv and "Pilot 1" in csv
    print("PASS: 60 balanced random assignments, briefing, test, result, ranking, admin and CSV")


if __name__ == "__main__":
    run()
