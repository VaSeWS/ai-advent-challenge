# День 28 — локальный RAG и сравнительный эксперимент

## Задание и воспроизведение

Использовать существующий RAG-индекс (`week-05/rag.db`), выполнить локальный поиск и генерацию, сравнить локальную и облачную модели по качеству, скорости и стабильности и подготовить видео. Сравнения должны использовать одинаковый frozen retrieval context; renderer и сохранённые отчёты сами по себе не доказывают успешный интерактивный сценарий.
Текущая модель генерации по умолчанию — выбранная пользователем `mlx-community/Qwen3.5-4B-4bit@0e7ffd5c629ef7719d4cbc04069232580bfa9d9c`. Активны профили A и B на одном pin; их числовые настройки не меняются. C сохранён только в архивных данных. Выбор основан на опубликованных benchmarks, а не на локальном сравнении моделей; результаты на другом оборудовании и backend не устанавливают скорость или RAG-качество на этом Mac. Новый A20, независимый обзор и CLI final data gate пройдены. Запись трёх SAVED-кадров завершена с exit status 0: `video/day-28-demo.mp4`, длительность 139.4 с, 2560×1550. Визуальная приёмка и загрузка остаются у parent; сохранённые кадры не являются свежим LIVE/TUI или task-memory доказательством.

## Текущее состояние playback и приёмки

Финальный `video/demo.sh` не подменяет NEW4B evidence старым 27B report и не делает новых cloud-вызовов. Перед показом кадров renderer проверяет: ровно 20 terminal attempts (`q01`–`q10`, два повтора) на каждом плече; все 20 фактически dispatched/captured `request_json`; выбранный absolute HF snapshot pin в каждом local request; настройки A; exact frozen-файл SHA-256 `099418d4659fa757d5ff4c762bc695a2edefd975797e883783037ea2daf80311`; фактические question/context/candidates, digest и documentary prompt; полный captured schema prefix и effective messages. Последовательные system messages нормализуются соединением через `\n\n`: это **effective instruction parity**, не raw wire identity. Для local требуется точная native `json_schema` с `strict=true`, name/schema из инструкции; существующий DeepSeek использует `json_object`. Профильные labels/usage metadata не заменяют фактический запрос.

Для каждого отчёта нужен independent all20 review, связанный с basename и SHA-256 **точных bytes** отчёта. Canonical contract — `rag-video-semantic-review-v1`, `review_status` = `ready_for_review` или `accepted`, `inputs` = массив `{path, sha256}`, `observations` = 20 уникальных `{question_id, repeat, terminal_status, strict_valid, semantic_verdict, citation_valid}`. Допустимые verdicts: `correct_answer`, `correct_refusal`, `incomplete`, `incorrect`, `unscorable`; failed attempts всегда `unscorable`, complete ответы обязательно оценены. `strict_valid` отражает lifecycle `complete`, не semantic correctness. `citation_valid` — bool для complete (refusal требует пустых citations), bool/null для errors; точные ID/quotes перепроверяются по фактическому context. Review должен также иметь независимое обоснование каждой оценки; renderer не заменяет semantic reviewer. `outcomes` содержит согласованные `planned=20`, `terminal=20`, `strict_valid`, `semantically_correct`, `correct_refusals`.

В финальном DeepSeek-плече **обязателен** неизменный `deepseek-A-semantic-review-corrected.json` с exact SHA-256 `4250633ebca9fc17a9a7e6dffe968df16c9a55c2f349fadf737e8d730add8a85`; alternate canonical cloud review не принимается. Canonical contract остаётся для новых local A/B reviews. Все 20 сохранённых cloud answer/fact assessments сопоставляются с exact report bytes. Это не новый cloud run и не автоматическая оценка по цитатам. DeepSeek report должен быть прежним `day28-deepseek-real-A-v1.jsonl`, SHA-256 `6316e178806961aa488ffdc796789b9c0065c46e4a73ce7b758468c805ae4279`. Полностью корректны 16/20 outputs; четыре complete q04/q05 outputs неполны. Нет переноса этих чисел в новый local report.

Новый selected-4B A завершил все 20 попыток без retries: 18 complete, две ошибки точности цитат; независимый review оценил 10/20 полностью корректными, две неполными, шесть неверными и две unscorable. CLI final gate для нового A и сохранённого DeepSeek прошёл на exact report/review bytes и всех captured requests. Это проверка данных, не приёмка свежей двухходовой TUI-сессии или видео.

Последний диагностический ordinary dry-run v5 не принят: `lane 2 failed: validate task state provenance: state quote "GT:NH" is not present in user input or prior user state`, exit status TUI 1. Полный failed pane сохранён до teardown. В новой сессии `cb9ff0b9-f541-4562-807d-be6c8e142ae3` нет chat turns/messages, оба lane state — `{}`; две завершённые native генерации не доказывают main/aux coverage. Backend естественно drained до idle, без новых запросов и retries. Validator не ослаблялся, prompt/code resolver не исправлялись. Это историческая неуспешная диагностика ordinary LIVE, не принятая как успешный сценарий и не блокер текущего SAVED-видео; новые ordinary LIVE-попытки для этой записи не выполнялись и не требуются.

Renderer пагинирует все значимые answers/citations, включая диагностические error bodies с меткой UNVALIDATED. Полная карточка captured provenance также пагинируется: она не вытесняет весь ответ из малого viewport. `--quality-summary` показывает independent quality, repeat **verdict/citation/lifecycle** agreement (не обещание общей стабильности) и generation-path wall time с доступным N/20; ошибки не фильтруются, missing times не заменяются нулями. `--resource-summary` отдельно показывает observed MLX allocation, process RSS, pressure/swap snapshots и missingness; remote resources неизвестны. Для A/B task templates намеренно различаются, shared schema/frozen context не означает одинаковый полный prompt.

Failed scalar/array final JSON и malformed citation values сохраняются как санитизированный текст с явной меткой **UNVALIDATED**, а не как valid answer/refusal/citation. Все значимые fragments проходят тот же полный wrapping/paging без обрезки; reasoning, raw prompts и посторонние object fields не выводятся. В объектах диагностики показываются только answer/status/citation-поля (либо связанные source metadata); пропущенные посторонние поля отмечаются явно.

Исторический standalone playback: `compare.py ... --historical-confound`. Renderer классифицирует captured protocol stage как pre-exact-instruction / pre-native-schema / mixed / unobserved и помечает NON-RANKING; missing capture не превращается в конкретный этап. `DAY28_ALLOW_HISTORICAL=1` больше не переключает финальное видео на архив: demo отказывает и предлагает отдельный archive renderer. Старые immutable experiments остаются неизменными.

Установите изолированное окружение вручную (demo scripts зависимости не устанавливают):

```sh
python3 -m venv "$HOME/.local/share/ai-advent/day28-renderer"
"$HOME/.local/share/ai-advent/day28-renderer/bin/python" -m pip install -r week-06/day-28/video/requirements.txt
```

Зависимости: Python 3.11+, `regex==2026.9.29`, `wcwidth==0.9.2`. `DAY28_PYTHON` может указывать на другой изолированный Python с этими requirements. Подготовьте **реальные** новые report/review paths после one-shot task acceptance; следующие env assignments — настройки запуска, не утверждение, что evidence уже создан:

```sh
RUN=.omp/task-execute/runs/week06-local-llm-20261011/evidence
export DAY28_LOCAL_REPORT="$RUN/day28-qwen35-4b-native-A-20261013-v1.jsonl"
export DAY28_LOCAL_REVIEW="$RUN/day28-qwen35-4b-A-semantic-review-20261014-v1.json"
export DAY28_CLOUD_REPORT="$RUN/day28-deepseek-real-A-v1.jsonl"
export DAY28_CLOUD_REVIEW="$RUN/deepseek-A-semantic-review-corrected.json"
export DAY28_FROZEN="$RUN/day28-frozen-real-v1.json"
PY="$HOME/.local/share/ai-advent/day28-renderer/bin/python"
# Offline, no inference:
"$PY" week-06/day-28/video/compare.py "$DAY28_LOCAL_REPORT" "$DAY28_CLOUD_REPORT" \
  --left-provider local --right-provider deepseek --left-profile A --right-profile A \
  --frozen "$DAY28_FROZEN" --left-review "$DAY28_LOCAL_REVIEW" \
  --right-review "$DAY28_CLOUD_REVIEW" --check-final-only
```

Тот же CLI с `--quality-summary --pause 12` показывает readable quality card, с `--resource-summary --pause 12` — actual resources. Для day29 используйте два local A/B report/review, `--right-provider local --left-profile A --right-profile B`, тот же frozen и `--require-selected-model`; активное плечо C не требуется. Plain `--summary` остаётся lifecycle/timing view, а не semantic score.

### Историческая ordinary LIVE-диагностика

Предыдущие попытки ordinary local RAG и их provenance/error outcomes остаются историческими диагностическими данными, а не доказательством успешного свежего 4B LIVE-сценария. В частности, последняя попытка v5 завершилась ошибкой provenance `GT:NH` (см. выше); validator не ослаблялся, и этот failed run не считается acceptance. Текущий одобренный demo не запускает ordinary TUI, не требует `DAY28_LIVE_EVIDENCE_DIR` или `DAY28_LIVE_AUTHORIZED`, и не заявляет свежую TUI/task-memory acceptance. Все три его кадра — playback неизменяемых сохранённых отчётов и reviews.

Историческая запись о двухходовой TUI-сессии на 27B приведена ниже отдельно; она не переносится на выбранный 4B pin и не является кадром текущего видео.

## План видео — week-06/day-28
Сценарий сравнивает сохранённые NEW4B A20 / EXISTING DeepSeek20 данные по качеству, времени и повторам. `video/demo.sh` проверяет immutable reports/reviews и frozen context перед показом; новых model/cloud-вызовов нет.
Кадры (все **SAVED**, не свежая TUI/task-memory демонстрация):
1. SAVED / NEW4B A и EXISTING DeepSeek A: q03, ответы и цитаты рядом → `demo_run "${renderer}--question q03 --repeat 1 --pause 4 --page-hold 6"`
2. SAVED / q09: фактический refusal/answer/error без предрешённого local исхода → `demo_run "${renderer}--question q09 --repeat 1 --pause 4 --page-hold 6"`
3. SAVED / independent quality, repeat agreement и generation-path wall time; denominator 20 → `demo_run "${renderer}--quality-summary --pause 6 --page-hold 6"`
Запись завершена: `week-06/day-28/video/day-28-demo.mp4`, 139.4 с, 2560×1550; `record.sh` завершился с кодом 0. Визуальная приёмка и загрузка ещё ожидают parent. До её подтверждения не заявлять публикацию и не распространять ссылку на видео.

---

## Отдельный native A на 27B — неизменная история

`day28-local-native-schema-A-20261013-v1.jsonl`, SHA-256 `82d63b443f5036fb44d3f10449b7a027157685d947c1d3ca6bb50339ae88ed3a`: 40 rows = 20 planned + 20 terminal; **10 complete + 10 error из 20**, только 13 actual captured requests. Independent historical review: 6 fully correct, 4 grounded-but-incomplete, 10 unscorable errors; q01/q03/q06 оба повтора корректны, q04/q05 оба повтора неполны. Q04 residual-risk qualifier не полностью присутствует в captured context; q05 essential caveat присутствует, но не включён в ответ. У 10 complete 12 exact valid citation items; assertion grounding не равно полной rubric correctness. Два q02 citation-validation errors, один cancelled q07/r1 и семь pre-dispatch errors не становятся refusals/answers.

Для 13 captures совпадают frozen context/candidates и effective instructions с DeepSeek; raw role-flattened messages различаются, local strict json_schema vs remote json_object. Недостающие captures: q07/r2, q08/r1-r2, q09/r1-r2, q10/r1-r2; исторический all20 captured gate **не пройден**. Это ограничение старого артефакта, не автоматический блокер нового отдельного 4B A20. Фактические настройки старого native A: `0/2048/16384/thinking-on/grounded`, не параметры прежнего TUI smoke. Исторические bytes и ошибки не изменяются и не повторяются.

## Свежий A с parity-инструкцией, но старым wire — архивный эксперимент

Отдельный post-instruction-parity A сохранён в `.omp/task-execute/runs/week06-local-llm-20261011/evidence/day28-local-parity-A-20261013-v1.jsonl`: 20 terminal attempts, 8 `complete` и 12 `error`. Его captured local request всё ещё использовал `response_format.type=json_object`; он не является native-json_schema baseline и не считается новым измерением после wire cutover. Он хранится отдельно и не заменяет предыдущие reports. Для выбранного 4B pin оба native-schema профиля A и B измерены на том же frozen-наборе; генеративного плеча C больше нет. Никаких новых semantic quality результатов или ranking здесь не заявляется.

## Старые замеры A — до точного равенства schema-инструкций

A выполнен на десяти frozen-вопросах по два повтора на плечо: 20 запланированных и 20 терминальных попыток у local и DeepSeek. Для каждой пары совпадали исходный вопрос и frozen context. Snapshot/build: `snapshot-ec91e5a8c85c8f5a8633844d7c58d8f4` / `3334022d-8fa3-4883-a5d9-4b34c0c95360`; structural retrieval `candidate/context 10/3`, threshold `0.25` (uncalibrated), `nomic-embed-text:latest`, 768 dimensions. Локальная модель — Qwen3.8-27B-4bit revision `10c35caafbb80f7dc6a7a432cdd11af10a6d4818`.

| Профиль | Локальные настройки | Результат из 20 |
|---|---|---|
| A | temperature `0`, max tokens `2048`, context/KV `16384`, thinking on, `grounded` | 2 строговалидных: оба корректные q09 refusals; 18 schema/length failures. |
| DeepSeek A | `deepseek-flash`, temperature `0`, max tokens `2048`, thinking off, `json_object` (captured HTTP request) | 20 строговалидных: 16 ответов и 4 корректных отказа; 20/23 ожидаемых факт-единиц присутствовали, 3 пропущены/неполны, противоречий нет; 24 валидные цитаты. Полностью корректны 8/10 вопросов. |

Ошибочные локальные тела — только диагностика, не ответы. DeepSeek использовал удалённый сервис; его KV/context и ресурсы сервера неизвестны. Парный набор не доказывает причинность отдельной настройки, а два повтора не обосновывают широкие выводы о стабильности.

Generation-path wall time (не чистое model compute): local A среднее/медиана `170.93/168.27` с, диапазон `71.383–251.710` с; DeepSeek A `1.732/1.747` с, диапазон `1.406–2.178` с. Общее время запусков — `3421.91` с и `36.04` с соответственно. Retrieval занял суммарно `3.72` с и не изолирован от старого TUI. Local MLX peak allocation `19.076–22.307 GB`, это не RSS; process RSS, system memory pressure и swap — отдельные метрики. Облачные ресурсы не наблюдались.

Immutable evidence хранится в `.omp/task-execute/runs/week06-local-llm-20261011/evidence/`: frozen inputs `day28-frozen-real-v1.json`; reports `day28-local-real-A-v1.jsonl`, `day28-deepseek-real-A-v1.jsonl`; semantic reviews `local-A-semantic-review-v1.json`, `deepseek-A-semantic-review-corrected.json`. Playback/summary:

```sh
RUN=.omp/task-execute/runs/week06-local-llm-20261011/evidence
PY="$HOME/.local/share/ai-advent/day28-renderer/bin/python"
"$PY" week-06/day-28/video/compare.py \
  "$RUN/day28-local-real-A-v1.jsonl" "$RUN/day28-deepseek-real-A-v1.jsonl" \
  --left-provider local --right-provider deepseek --question q01 --repeat 1 --historical-confound
"$PY" week-06/day-28/video/compare.py \
  "$RUN/day28-local-real-A-v1.jsonl" "$RUN/day28-deepseek-real-A-v1.jsonl" \
  --left-provider local --right-provider deepseek --summary --historical-confound
```

`--summary` показывает lifecycle/timing, не semantic review. Исторические A-данные не являются финальной оценкой исправленного контракта.

## Интерактивный RAG: сохранённые ошибки и проверенный native-schema путь

Первый реальный ordinary local RAG turn завершился ошибкой lane 2: цитата пришла строкой вместо обязательного объекта. Сессия `46dac513-d674-4254-9c7f-b0ad75a947ac` завершилась с exit 1; follow-up и retry не отправлялись, turn не был зафиксирован. Позднее отдельные одобренные профили с точной schema-инструкцией и лимитами 1536/4096 также упёрлись в `finish_reason="length"` на resolver. Все эти исходы сохранены, а не переименованы в успешные ответы.

Проверка источника в исходном эксперименте подтвердила, что DeepSeek получал переданную JSON Schema в system instructions, а локальный запрос шёл через `json_object`; отдельный post-instruction-parity A также сохранил эту инструкцию, но всё ещё был `json_object` на wire. Поэтому historical evidence охватывает разные этапы и renderer называет этап только по фактически захваченным данным. Новые cloud-вызовы не одобрены: новых облачных запросов не делать; A уже содержит 20 DeepSeek попыток. Не повторять старые попытки и не удалять/перезаписывать исходные observations.

После отдельного одобренного перехода на native `json_schema` реальная TUI-сессия `36c30fc4-8bdb-47d0-8b26-ea2ae75e65f0` завершила два парных хода за `279.01` с: восемь сообщений, семь штатных main/aux-вызовов, task memory во второй линии, cloud keys unset. Первый вопрос получил приоритеты `0 / -1 / -2` с проверяемой точной цитатой; follow-up был разрешён в самостоятельный запрос и получил отказ по недостаточности фактически извлечённого контекста в обеих линиях. Этот ранее пройденный runtime относится к тогдашнему 27B backend, а не к выбранному 4B pin; это не результат 20-контрольного benchmark и не запись видео.

Для нового selected-4B отчёта A используется существующий одобренный frozen evidence, совпадающий с набором для DeepSeek A. Не запускайте `rag-experiment freeze` и не создавайте замену: этот артефакт неизменяемый.

```sh
FROZEN=.omp/task-execute/runs/week06-local-llm-20261011/evidence/day28-frozen-real-v1.json
# Approved SHA-256: 099418d4659fa757d5ff4c762bc695a2edefd975797e883783037ea2daf80311
go run ./week-05 rag-experiment run -frozen "$FROZEN" \
  -providers local -profiles A -repeats 2 -out NEW_QWEN35_4B_A.jsonl
```

Новые native-schema отчёты выбранного 4B pin используют только A и B, тот же frozen-файл и отдельные exclusive-create пути. Оба плеча завершили все десять controls и оба повтора, без retries и исключения ошибок из знаменателя. All20 captured native schema/effective prompt/frozen gates и независимые обзоры пройдены; semantic correctness не следует из schema acceptance.

Ранее пройденный двухходовый путь относится к 27B и не доказывает свежую ordinary 4B-сессию. Новый A20 и exact three-shot SAVED approval получены; запись завершена, визуальная приёмка и загрузка остаются у parent. Целевой файл `week-06/day-28/video/day-28-demo.mp4`; назначение на Диске `/ai-advent-challenge/week-6/day-28-demo.mp4` (не публичный URL).
