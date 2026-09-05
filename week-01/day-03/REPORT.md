# Day 3 — отчёт: 4 способа рассуждения на одной задаче

Модель: `openai/gpt-oss-20b` (Groq), `max_tokens=1024`. Задача одна и та
же для всех способов, каждый способ обязан вернуть код решения на Python.

Доп. инструкции (требование кода, длина, "решай пошагово", инструкция для
мета-промпта) идут через `system`-роль, а не подмешаны в текст задачи.
`task` (user-сообщение) — чистое условие, без прочих указаний.

## Задача

[LeetCode 3904 — Smallest Stable Index II](https://leetcode.com/problems/smallest-stable-index-ii/)

```
You are given an integer array nums of length n and an integer k.

For each index i, define its instability score as max(nums[0..i]) - min(nums[i..n - 1]).
An index i is called stable if its instability score is less than or equal to k.

Return the smallest stable index. If no such index exists, return -1.

Example 1: nums = [5,0,1,4], k = 3 → 3
Example 2: nums = [3,2,1], k = 1 → -1
Example 3: nums = [0], k = 0 → 0

Constraints:
1 <= nums.length <= 10^5
0 <= nums[i] <= 10^9
0 <= k <= 10^9
```

**baseSystemPrompt** (общий для способов 1, 2, 4):
```
Реши задачу и верни код решения на Python: функция
def stable_index(nums: list[int], k: int) -> int:
Требование: код должен работать за O(n) по времени (n до 10^5), без пересчёта
max/min с нуля на каждом индексе. Будь краток: обоснование не длиннее
нескольких предложений, без формальных доказательств теорем. Код функции
обязателен и должен быть полным.
```

Корректный алгоритм: `prefix_max[i]` (проход слева направо) и
`suffix_min[i]` (проход справа налево), затем линейный поиск первого `i`,
где `prefix_max[i] - suffix_min[i] <= k`.

---

## 1) Прямой ответ

**System:** `baseSystemPrompt` (как есть).
**User:** задача.

**Ответ:**
```
Compute pref_max[i] and suf_min[i] once, then scan for the first i where
pref_max[i] - suf_min[i] <= k. O(n) time, O(n) memory.
```
```python
def stable_index(nums: list[int], k: int) -> int:
    n = len(nums)
    if n == 0:
        return -1

    pref_max = [0] * n
    suf_min = [0] * n

    cur = nums[0]
    for i in range(n):
        if nums[i] > cur:
            cur = nums[i]
        pref_max[i] = cur

    cur = nums[-1]
    for i in range(n - 1, -1, -1):
        if nums[i] < cur:
            cur = nums[i]
        suf_min[i] = cur

    for i in range(n):
        if pref_max[i] - suf_min[i] <= k:
            return i
    return -1
```

---

## 2) Пошаговое рассуждение

**System:** `baseSystemPrompt + "\n\nРешай пошагово: распиши ход рассуждений перед финальным кодом."`
**User:** задача.

**Ответ:**
```
1. max_left[i] = max(nums[0..i]) (прямой проход)
2. min_right[i] = min(nums[i..n-1]) (обратный проход)
3. Первый i, где max_left[i] - min_right[i] <= k, — ответ; иначе -1.
Всё O(n) по времени и памяти.
```
```python
def stable_index(nums: list[int], k: int) -> int:
    n = len(nums)
    if n == 0:
        return -1

    max_left = [0] * n
    max_left[0] = nums[0]
    for i in range(1, n):
        max_left[i] = max(nums[i], max_left[i - 1])

    min_right = [0] * n
    min_right[-1] = nums[-1]
    for i in range(n - 2, -1, -1):
        min_right[i] = min(nums[i], min_right[i + 1])

    for i in range(n):
        if max_left[i] - min_right[i] <= k:
            return i
    return -1
```

Как и в прошлом прогоне (LIS) — план из нескольких пунктов, тот же код по
сути, что и в прямом ответе. Разница почти незаметна.

---

## 3) Мета-промпт

**System (для генерации промпта):** `"Составь самодостаточный промпт (включая формулировку задачи), который поможет другой LLM максимально точно решить следующую задачу. В ответе выведи только текст промпта, без пояснений."`
**User:** задача.

### 3a) Сгенерированный промпт

```
Task
You are given an integer array nums of length n (1 ≤ n ≤ 10⁵) and an
integer k (0 ≤ k ≤ 10⁹). For each index i define its instability score as
instability(i) = max(nums[0..i]) – min(nums[i..n-1]).
An index i is stable if instability(i) ≤ k.
Return the smallest stable index. If no such index exists, return -1.

Examples: [те же 3 примера]

Requirements
1. Implement a function smallestStableIndex(nums: List[int], k: int) -> int
   in Python 3.
2. The algorithm must run in O(n) time and O(1) extra space (aside from
   the input array).
3. Provide a clear, commented implementation and a brief explanation of
   the algorithm, including its time and space complexity.

Deliverable
A single Python file containing the function and an optional test
harness that demonstrates correctness on the examples above.
```

### 3b) Решение по сгенерированному промпту

**User:** только текст промпта выше (без нашего `baseSystemPrompt`).

**Ответ: пустой.** Модель не вернула ничего — ни обоснования, ни кода.

**Разбор причины.** Сгенерированный промпт сам придумал требование,
которого не было в исходной задаче: *"O(n) time and O(1) extra space"*.
Правильный алгоритм для этой задачи требует `O(n)` дополнительной памяти
(массивы `prefix_max`/`suffix_min`) — сжать до `O(1)` для произвольного
`k` в общем случае нельзя, не теряя возможность отвечать по префиксу и
суффиксу одновременно. Модель, видимо, не смогла согласовать это
самопротиворечие в рамках лимита `max_tokens=1024` и не выдала ответ.
Это не разовая случайность метода — воспроизводимо: при повторном
прогоне (после увеличения бюджета с 900 до 1024 токенов) 3b либо
обрывался на середине формального доказательства без кода, либо, как
здесь, не отвечал вовсе.

---

## 4) Панель экспертов

**System:** `baseSystemPrompt + "\n\n" + <персона>`.
**User:** задача (без изменений).

### Аналитик

**Персона:** `Ты аналитик. Разбери задачу с точки зрения корректности и граничных случаев, дай решение и обоснование.`

**Ответ:**
```python
def stable_index(nums: list[int], k: int) -> int:
    n = len(nums)
    pref_max = [0] * n
    cur_max = nums[0]
    for i in range(n):
        cur_max = max(cur_max, nums[i])
        pref_max[i] = cur_max

    suff_min = [0] * n
    cur_min = nums[-1]
    for i in range(n - 1, -1, -1):
        cur_min = min(cur_min, nums[i])
        suff_min[i] = cur_min

    for i in range(n):
        if pref_max[i] - suff_min[i] <= k:
            return i
    return -1
```
Разобрал граничные случаи: `n == 1` и большие значения (Python-инты не
переполняются).

### Инженер

**Персона:** `Ты инженер-программист. Дай практичное решение с оценкой временной и пространственной сложности.`

**Ответ:**
```python
def stable_index(nums: list[int], k: int) -> int:
    n = len(nums)
    if n == 0:
        return -1

    prefix_max = [0] * n
    cur_max = nums[0]
    for i in range(n):
        if nums[i] > cur_max:
            cur_max = nums[i]
        prefix_max[i] = cur_max

    suffix_min = [0] * n
    cur_min = nums[-1]
    for i in range(n - 1, -1, -1):
        if nums[i] < cur_min:
            cur_min = nums[i]
        suffix_min[i] = cur_min

    for i in range(n):
        if prefix_max[i] - suffix_min[i] <= k:
            return i
    return -1
```
Complexity: Time `O(n)` (forward + backward pass + scan), Space `O(n)`
(два вспомогательных массива).

### Критик

**Персона:** `Ты критик. Укажи на распространённые ошибки в решении этой задачи и дай собственный ответ с обоснованием, почему он верный.`

**Ответ:** таблица типичных ошибок —

| Ошибка | Почему ломается |
|---|---|
| Пересчёт max/min внутри цикла | O(n²), TLE при n = 10⁵ |
| Один проход слева для max и min | suffix min зависит от будущих элементов — прямым проходом не получить |
| Off-by-one в суффиксном минимуме | `suffix_min[i] = min(suffix_min[i+1], nums[i])` без правильной границы даёт `suffix_min[n] = INF`, все индексы "проваливаются" |
| Не обработан `n == 1` отдельно | работает и так, но легко словить баг в границах цикла |
| `max()`/`min()` внутри цикла на каждой итерации | тот же O(n²) эффект + оверхед вызовов функций |

```python
def stable_index(nums: list[int], k: int) -> int:
    n = len(nums)
    prefix_max = [0] * n
    cur_max = nums[0]
    for i in range(n):
        if nums[i] > cur_max:
            cur_max = nums[i]
        prefix_max[i] = cur_max

    suffix_min = [0] * n
    cur_min = nums[-1]
    for i in range(n - 1, -1, -1):
        if nums[i] < cur_min:
            cur_min = nums[i]
        suffix_min[i] = cur_min

    for i in range(n):
        if prefix_max[i] - suffix_min[i] <= k:
            return i
    return -1
```
Как и в прошлом прогоне (LIS) — единственный эксперт, перечисливший
конкретные баги реализации, а не только правильный алгоритм.

---

## Проверка корректности

5 из 6 кодов (все, кроме пустого 3b) извлечены и прогнаны через
`python3`: 3 примера из условия + 500 случайных тестов против
brute-force.

```
1_direct:        PASS (examples + 500 random cases)
2_step_by_step:  PASS (examples + 500 random cases)
4_analyst:       PASS (examples + 500 random cases)
4_engineer:      PASS (examples + 500 random cases)
4_critic:        PASS (examples + 500 random cases)

ALL PASS
```

## Сравнение

| Способ | Код | Обоснование |
|---|---|---|
| 1. Прямой | верен | 1-2 предложения |
| 2. Пошаговый | верен | план из 3-4 пунктов, почти идентичен прямому |
| 3. Мета-промпт | **отсутствует** | сгенерированный промпт добавил невыполнимое требование (O(1) памяти) и модель не ответила вовсе |
| 4. Аналитик | верен | граничные случаи |
| 4. Инженер | верен | оценка сложности |
| 4. Критик | верен | единственный — таблица конкретных типичных багов |

**Итог: перенос доп. инструкций в system prompt не изменил качество кода**
там, где system prompt реально использовался (способы 1, 2, 4) — все 5
решений идентичны по алгоритму и проходят тесты. Разница по-прежнему в
сопроводительном тексте, и критик снова единственный дал содержательный
разбор ошибок, а не пересказ алгоритма.

**Мета-промпт — единственный способ, который на этой задаче сломался
полностью.** Когда модель формулирует промпт для самой себя без нашего
`baseSystemPrompt` (по замыслу задания — способ 3 должен работать только
на сгенерированном промпте), она добавляет условия от себя — в первом
прогоне это была просьба не объяснять вообще, во втором — трудновыполнимое
требование O(1) памяти. В обоих случаях результат хуже, чем у остальных
3 способов: это единственный метод, продемонстрировавший нестабильность
результата между запусками на одной и той же задаче.

## Инфраструктурная проблема (не про задачу)

Прогон всех 7 запросов подряд периодически упирался в лимит Groq free
tier — 8000 токенов/минуту, обычно на последнем (7-м) вызове, если сумма
предыдущих ответов вышла длиннее обычного. Решалось точечным повторным
запросом только упавшего вызова после паузы, без перезапуска всего
скрипта.
