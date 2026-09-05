# Day 3 — отчёт: 4 способа рассуждения на одной задаче

Модель: `openai/gpt-oss-20b` (Groq), `max_tokens=900`. Задача одна и та же
для всех способов, каждый способ обязан вернуть код решения на Python.

## Задача

[LeetCode 3904 — Smallest Stable Index II](https://leetcode.com/problems/smallest-stable-index-ii/)

```
You are given an integer array nums of length n and an integer k.

For each index i, define its instability score as max(nums[0..i]) - min(nums[i..n - 1]).

In other words:
max(nums[0..i]) is the largest value among the elements from index 0 to index i.
min(nums[i..n - 1]) is the smallest value among the elements from index i to index n - 1.
An index i is called stable if its instability score is less than or equal to k.

Return the smallest stable index. If no such index exists, return -1.

Example 1:
Input: nums = [5,0,1,4], k = 3
Output: 3

Example 2:
Input: nums = [3,2,1], k = 1
Output: -1

Example 3:
Input: nums = [0], k = 0
Output: 0

Constraints:
1 <= nums.length <= 10^5
0 <= nums[i] <= 10^9
0 <= k <= 10^9
```

Требование к каждому способу, вшитое в промпт: код решения на Python,
функция `def stable_index(nums: list[int], k: int) -> int`, за O(n)
(без пересчёта max/min с нуля на каждом индексе), плюс инструкция быть
кратким (без формальных доказательств — иначе модель съедала весь
токен-бюджет на один вызов).

Корректный алгоритм: `prefix_max[i]` (проход слева направо) и
`suffix_min[i]` (проход справа налево), затем линейный поиск первого `i`,
где `prefix_max[i] - suffix_min[i] <= k`.

---

## 1) Прямой ответ (без инструкций)

**Промпт (user):** задача + требование кода + "будь краток" (текст выше).

**Ответ:**
```
Обоснование: строим pref_max (проход слева-направо) и suf_min (проход
справа-налево), затем ищем первый i, где pref_max[i] - suf_min[i] <= k.
Время O(n), память O(n).
```
```python
from typing import List

def stable_index(nums: List[int], k: int) -> int:
    n = len(nums)
    if n == 0:
        return -1

    pref_max = [0] * n
    current_max = nums[0]
    pref_max[0] = current_max
    for i in range(1, n):
        if nums[i] > current_max:
            current_max = nums[i]
        pref_max[i] = current_max

    suf_min = [0] * n
    current_min = nums[-1]
    suf_min[-1] = current_min
    for i in range(n - 2, -1, -1):
        if nums[i] < current_min:
            current_min = nums[i]
        suf_min[i] = current_min

    for i in range(n):
        if pref_max[i] - suf_min[i] <= k:
            return i
    return -1
```

---

## 2) Пошаговое рассуждение

**Промпт (user):** задача + `"\n\nРешай пошагово: распиши ход рассуждений перед финальным кодом."`

**Ответ:**
```
Пошаговый план:
1. Вычислить pref_max[i] = max(nums[0..i]).
2. Вычислить suf_min[i] = min(nums[i..n-1]) (проход справа налево).
3. Пройти по массиву от 0 до n-1, вернуть первый i с
   pref_max[i] - suf_min[i] <= k, иначе -1.
Все операции O(n) по времени и памяти.
```
```python
from typing import List

def stable_index(nums: List[int], k: int) -> int:
    n = len(nums)
    pref_max = [0] * n
    current = nums[0]
    pref_max[0] = current
    for i in range(1, n):
        if nums[i] > current:
            current = nums[i]
        pref_max[i] = current

    suf_min = [0] * n
    current = nums[-1]
    suf_min[-1] = current
    for i in range(n - 2, -1, -1):
        if nums[i] < current:
            current = nums[i]
        suf_min[i] = current

    for i in range(n):
        if pref_max[i] - suf_min[i] <= k:
            return i
    return -1
```

Отличие от прямого ответа минимально — план из 3 пунктов перед тем же
кодом. На этой задаче пошаговость не добавила ничего практического: сама
задача достаточно короткая, чтобы модель решала её "с одного взгляда".

---

## 3) Мета-промпт

### 3a) Промпт для генерации промпта (user)

Инструкция: составить самодостаточный промпт (с условием задачи), который
поможет другой LLM максимально точно решить задачу; вернуть только текст
промпта.

### Сгенерированный промпт (ответ модели)

```
Write a Python function

def stable_index(nums: list[int], k: int) -> int:
    ...

that returns the smallest index i such that
max(nums[0..i]) - min(nums[i..n-1]) <= k.
If no such index exists return -1.
Constraints: 1 <= len(nums) <= 10^5, 0 <= nums[i] <= 10^9, 0 <= k <= 10^9.
The solution must run in O(n) time and O(n) auxiliary space.
Do not recompute the maximum or minimum from scratch for each index; use
pre-computed prefix maxima and suffix minima.
Provide only the complete function code, no additional explanation or output.
```

### 3b) Решение по сгенерированному промпту

**Ответ:**
```python
def stable_index(nums: list[int], k: int) -> int:
    n = len(nums)
    if n == 0:
        return -1

    prefix_max = [0] * n
    prefix_max[0] = nums[0]
    for i in range(1, n):
        prefix_max[i] = prefix_max[i - 1] if prefix_max[i - 1] > nums[i] else nums[i]

    suffix_min = [0] * n
    suffix_min[-1] = nums[-1]
    for i in range(n - 2, -1, -1):
        suffix_min[i] = suffix_min[i + 1] if suffix_min[i + 1] < nums[i] else nums[i]

    for i in range(n):
        if prefix_max[i] - suffix_min[i] <= k:
            return i
    return -1
```

Сгенерированный промпт получился строже исходного (сам добавил "output
only the function code, no explanation") — и модель послушалась: единственный
из 7 ответов вообще без обоснования, только код.

---

## 4) Панель экспертов

Каждая роль — отдельный запрос: `system` = описание роли, `user` = задача
(без изменений).

### Аналитик

**System:** `Ты аналитик. Разбери задачу с точки зрения корректности и граничных случаев, дай решение и обоснование.`

**Ответ:**
```
Алгоритм: pref_max (max в [0..i]), suff_min (min в [i..n-1]), первый i с
pref_max[i] - suff_min[i] <= k, иначе -1. O(n) времени и памяти.
```
```python
from typing import List

def stable_index(nums: List[int], k: int) -> int:
    n = len(nums)
    pref_max = [0] * n
    pref_max[0] = nums[0]
    for i in range(1, n):
        pref_max[i] = max(pref_max[i-1], nums[i])

    suff_min = [0] * n
    suff_min[-1] = nums[-1]
    for i in range(n-2, -1, -1):
        suff_min[i] = min(suff_min[i+1], nums[i])

    for i in range(n):
        if pref_max[i] - suff_min[i] <= k:
            return i
    return -1
```
Граничные случаи, отмеченные моделью: `n == 1` (работает как обычный
проход), `k == 0` (допускает только индексы с равными max/min).

### Инженер

**System:** `Ты инженер-программист. Дай практичное решение с оценкой временной и пространственной сложности.`

**Ответ:**
```python
def stable_index(nums: list[int], k: int) -> int:
    n = len(nums)
    if n == 0:
        return -1

    pref_max = [0] * n
    cur_max = nums[0]
    for i in range(n):
        if nums[i] > cur_max:
            cur_max = nums[i]
        pref_max[i] = cur_max

    suff_min = [0] * n
    cur_min = nums[-1]
    for i in range(n - 1, -1, -1):
        if nums[i] < cur_min:
            cur_min = nums[i]
        suff_min[i] = cur_min

    for i in range(n):
        if pref_max[i] - suff_min[i] <= k:
            return i
    return -1
```
Complexities: Time `O(n)` (один проход на каждый массив + линейный
поиск), Space `O(n)` (два вспомогательных массива).

### Критик

**System:** `Ты критик. Укажи на распространённые ошибки в решении этой задачи и дай собственный ответ с обоснованием, почему он верный.`

**Ответ:**
```
Критика типичных ошибок:
1. Пересчёт max/min в каждой итерации -> O(n^2).
2. Неправильный срез (min(nums[i+1:]) исключает элемент i).
3. Забывают, что справа нужен минимум, а не максимум.
4. Не проверяют suff_min[n-1] отдельно (последний элемент).
5. Возвращают первый подходящий индекс, но не убеждаются, что он
   действительно самый маленький (например, при обходе не слева направо).
```
```python
def stable_index(nums: list[int], k: int) -> int:
    n = len(nums)
    if n == 0:
        return -1

    pref_max = [0] * n
    pref_max[0] = nums[0]
    for i in range(1, n):
        pref_max[i] = pref_max[i-1] if pref_max[i-1] > nums[i] else nums[i]

    suff_min = [0] * n
    suff_min[-1] = nums[-1]
    for i in range(n-2, -1, -1):
        suff_min[i] = suff_min[i+1] if suff_min[i+1] < nums[i] else nums[i]

    for i in range(n):
        if pref_max[i] - suff_min[i] <= k:
            return i
    return -1
```
Единственный из трёх экспертов, кто явно перечислил конкретные баги
(включая реалистичную ошибку со срезом `nums[i+1:]`), а не просто описал
правильный алгоритм.

---

## Проверка корректности

Каждый из 7 кодов извлечён и прогнан через `python3`: 3 примера из условия
+ 500 случайных тестов (`n` до 30, значения 0-15) против brute-force
(`max(nums[:i+1]) - min(nums[i:]) <= k`).

```
1_direct:        PASS (examples + 500 random cases)
2_step_by_step:  PASS (examples + 500 random cases)
3b_meta_prompt:  PASS (examples + 500 random cases)
4_analyst:       PASS (examples + 500 random cases)
4_engineer:      PASS (examples + 500 random cases)
4_critic:        PASS (examples + 500 random cases)

ALL PASS
```

Все 6 вариантов кода (прямой, пошаговый и мета-промпт совпадают, плюс 3
эксперта) реализуют один и тот же корректный алгоритм и проходят все
тесты — ошибок в коде не нашлось ни у одного способа.

## Сравнение

Задача оказалась достаточно простой для `gpt-oss-20b`, чтобы все способы
дали рабочий код с первого раза — корректность не разделяет способы.
Разница — в форме и глубине сопровождающего текста:

| Способ | Код верен | Обоснование |
|---|---|---|
| 1. Прямой | да | 2-3 предложения |
| 2. Пошаговый | да | план из 3 пунктов, почти идентичен прямому |
| 3. Мета-промпт | да | без обоснования вообще — сгенерированный промпт сам запретил объяснения |
| 4. Аналитик | да | явно разобрал граничные случаи (n=1, k=0) |
| 4. Инженер | да | явная оценка сложности времени/памяти |
| 4. Критик | да | единственный перечислил конкретные типичные баги (в т.ч. с некорректным срезом) |

**Наиболее ценный способ для code review — панель экспертов, роль
критика**: единственный ответ, где модель явно указала на реалистичные
ошибки реализации (неправильный срез, забытая граница массива), а не
просто пересказала правильный алгоритм. Пошаговое рассуждение на этой
задаче почти не отличалось от прямого ответа — задача слишком
компактная, чтобы разница в промпте проявилась в решении.

Мета-промпт показал обратный эффект по сравнению с задачей дня 3 на LIS:
там сгенерированный промпт просил только число, здесь — только код без
объяснений. В обоих случаях модель, генерируя промпт для самой себя,
делает его строже и суше, чем то, что просил бы человек.

## Инфраструктурная проблема (не про задачу)

Прогон всех 7 запросов подряд упирался в лимит Groq free tier — 8000
токенов/минуту. Без ограничения длины ответа модель на некоторых
вызовах уходила в формальные доказательства теорем (5000-6000 токенов
за один ответ) и рейт-лимит срабатывал уже на 2-3 запросе. Исправлено
добавлением `max_tokens=900` и инструкции "будь краток, без формальных
доказательств" в промпт — после этого весь прогон из 7 запросов
укладывается в бюджет.
