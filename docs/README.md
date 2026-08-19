# tutu-smile-doc

Сайт документации по исходному коду [tutu-smile](../tutu-smile-app). Один бинарник, только stdlib, доки и вьювер встроены через `go:embed`.

```bash
go run .            # http://localhost:8090
go run . -addr :9000
```

Страницы лежат в `docs/*.md`. Новая страница: файл в `docs/` плюс строка в массиве `PAGES` в `web/index.html`.

Markdown рендерит [marked](https://github.com/markedjs/marked) с CDN на клиенте; без интернета страницы не отрисуются (сами .md доступны по `/docs/<имя>.md`).
