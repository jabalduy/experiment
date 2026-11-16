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
        assert status == 200 and "name=\"name\"" in landing and "pre_1" not in landing
        status, url, briefing = request(person, "/start", {
            "name": "Pilot" + str(number),
            "consent": "yes",
        })
        assert status == 200 and "/briefing" in url, url
        assert "Продолжить" in briefing and "pre_1" in briefing
        is_ranking = "Соревновательный режим" in briefing
        assigned_groups.append(is_ranking)
        if number == 2:
            assert assigned_groups[0] != assigned_groups[1], "Balanced assignment mismatch"

        status, url, blocked = request(person, "/test")
        assert "/briefing" in url and "answer_q" not in blocked

        status, url, page = request(person, "/begin", {"pre_" + str(i): "2" for i in range(1, 21)})
        assert "/ready" in url and "quizForm" not in page and "Начать задания" in page
        status, url, page = request(person, "/launch", {})
        assert "/test" in url and "quizForm" in page
        assert "Таблица лидеров" not in page
        assert "Соревновательный режим" not in page

        # Question IDs and option indices are read from the quiz HTML.
        assert "Введите только число" in page and "Введите только букву" in page
        names = re.findall(r'name="answer_([^"]+)"', page)
        assert set(names) == {"q1", "q2", "q3", "q4", "q5"}, names
        answers = {"answer_q1": " 29 ", "answer_q2": "1", "answer_q3": " х ", "answer_q4": " 148 ", "answer_q5": "2"}
        answers.update({"time_" + name: "250" for name in names})
        status, url, result = request(person, "/finish", answers)
        assert status == 200 and "/result" in url
        assert "Ваш результат" in result and "5 <small>/ 5</small>" in result
        assert ("Таблица лидеров" in result) == is_ranking

        status, url, _ = request(person, "/finish", answers)
        assert "/result" in url, "Second submission should redirect to result"
        if is_ranking:
            status, _, ranking = request(person, "/api/leaderboard")
            assert "Pilot" + str(number) in ranking
        else:
            expect_forbidden(person, "/api/leaderboard")

        status, url, survey = request(person, "/survey")
        assert "/survey" in url and "post_20" in survey
        status, url, thanks = request(person, "/survey", {"post_" + str(i): "3" for i in range(1, 21)})
        assert "/thanks" in url and "Благодарим за участие" in thanks
        assert "thank-you-cat" not in thanks and "Спасибо, что помогаете науке" not in thanks

    # Signed manual links must be visible only in admin and bypass random quota.
    headers = {"X-Admin-Key": ADMIN}
    status, _, dashboard = request(admin, "/admin", headers=headers)
    assert status == 200 and "Ссылки для приглашения" in dashboard
    import html
    for group in ("ranking", "control"):
        match = re.search(r'value="([^"]*/join/' + group + r'\?key=[a-f0-9]+)"', dashboard)
        assert match, "Missing signed manual link: " + group
        link = html.unescape(match.group(1))
        if group == "control": control_link = link
        path = urllib.parse.urlsplit(link).path + "?" + urllib.parse.urlsplit(link).query
        visitor = client()
        status, _, landing = request(visitor, path)
        assert status == 200 and 'name="invite_group"' in landing
        key = urllib.parse.parse_qs(urllib.parse.urlsplit(link).query)["key"][0]
        status, url, briefing = request(visitor, "/start", {
            "name": "Manual" + group, "consent": "yes",
            "invite_group": group, "invite_key": key,
        })
        assert "/briefing" in url
        assert ("Соревновательный режим" in briefing) == (group == "ranking")
    try:
        request(admin, "/join/ranking")
    except urllib.error.HTTPError as exc:
        assert exc.code == 404
    else:
        raise AssertionError("Unsigned group link should be rejected")

    # 60 registrations must produce exactly 30 assignments per condition.
    # The first two users above already occupy one place in each group.
    totals = {True: 1, False: 1}
    for number in range(3, 61):
        person = client()
        status, url, briefing = request(person, "/start", {
            "name": "Pilot" + str(number),
            "consent": "yes",
        })
        assert "/briefing" in url
        totals["Соревновательный режим" in briefing] += 1
    assert totals == {True: 30, False: 30}, totals

    # Additional registrations remain possible after the random cohort is full.
    visitor = client()
    status, url, briefing = request(visitor, "/start", {"name": "ExtraControl", "consent": "yes", "invite_group": "control", "invite_key": urllib.parse.parse_qs(urllib.parse.urlsplit(control_link).query)["key"][0]})
    assert "/briefing" in url and "Соревновательный режим" not in briefing

    headers = {"X-Admin-Key": ADMIN}
    status, _, dashboard = request(admin, "/admin", headers=headers)
    assert status == 200 and "Pilot1" in dashboard and "Pilot2" in dashboard
    assert "Сравнение групп" in dashboard
    status, _, csv = request(admin, "/admin/export", headers=headers)
    assert status == 200 and "pre_answers_1_4" in csv and "Pilot1" in csv
    print("PASS: 60 balanced random assignments, briefing, test, result, ranking, admin and CSV")


if __name__ == "__main__":
    run()
