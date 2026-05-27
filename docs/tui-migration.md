# go-tui TUI Migration — Research & Plan

> Investigación completa para migrar `transport/cli/` a go-tui.
> Estado: **Pendiente** — a retomar cuando el Khan lo indique.

---

## ¿Qué es go-tui?

Framework TUI declarativo para Go con sintaxis `.gsx` (similar a templ/JSX) que compila a Go puro.
- Flexbox layout real (row/column/justify/align/gap/padding/margin)
- Estado reactivo via `State[T]` genérico con re-rendering automático
- Double-buffered rendering (diff-based, mínimas escrituras al terminal)
- Solo deps: `golang.org/x/{sys,tools}` — pure Go, zero CGO

**Repo:** https://github.com/grindlemire/go-tui  
**Docs:** https://go-tui.dev  
**Estado:** Pre-1.0 — APIs pueden cambiar

---

## ⚠️ Advertencia crítica

**Pre-1.0** — el README dice explícitamente:
> *"go-tui is under active development. Some APIs may evolve as the project matures."*

**Mitigación:** Pin a versión específica en `go.mod`. No usar `@latest` en producción.

---

## Lo que tiene que necesitamos

### 1. Streaming text nativo — `StreamWriter`

```go
// En el ai-chat example oficial:
c.streamWriter = c.app.StreamAbove()
c.streamWriter.WriteGradient(ev.Text, agentGradient)  // token a token
c.streamWriter.Close()
```

`app.StreamAbove()` devuelve un `StreamWriter` que imprime sobre el input area, token por token.
El race condition spinner/content que tuvimos → **eliminado** por el double-buffer.
Gradients incluidos de serie.

### 2. Channel Watcher — conecta directo con nuestros eventos

```go
func (c *chat) Watchers() []tui.Watcher {
    return []tui.Watcher{
        tui.NewChannelWatcher(c.eventCh, c.onStreamEvent),
    }
}
```

Nuestro `agent.Event` → canal → `ChannelWatcher` → handler. Es exactamente nuestro patrón de eventos.

### 3. Input multiline con `<textarea>`

```gsx
<textarea
    ref={c.textareaRef}
    autoFocus={true}
    placeholder="Type a message..."
    border={tui.BorderRounded}
    onSubmit={c.submit}
/>
```

Reemplaza nuestro `rawinput.go` completo (100 líneas).

### 4. Key events limpios

```go
func (c *chat) KeyMap() tui.KeyMap {
    return tui.KeyMap{
        tui.On(tui.Rune('q'), func(ke tui.KeyEvent) { ke.App().Stop() }),
        tui.OnStop(tui.KeyEscape, func(ke tui.KeyEvent) { c.cancelStream() }),
        tui.OnStop(tui.Rune('c').Ctrl(), func(ke tui.KeyEvent) { ke.App().Stop() }),
    }
}
```

Nuestros `/model`, `/thinking`, `/clear` → KeyMap + parsing del texto del textarea.

### 5. Inline mode — comportamiento idéntico al nuestro

```go
tui.WithInlineHeight(3)  // crece dinámicamente con el contenido
```

No alternate screen. El output scrollea arriba como terminal normal.
El `ai-chat` example oficial usa exactamente este modo.

### 6. PrintAboveln para output estático (tool calls, resultados)

```go
c.app.PrintAboveln("⚡ bash ls -la")
c.app.PrintAboveln("  ✓ result [19ms]")
```

### 7. Scrolling nativo con auto-scroll y stick-to-bottom

```gsx
<div scrollable={tui.ScrollVertical} scrollOffset={0, s.scrollY.Get()}>
    for _, line := range s.lines.Get() {
        <span>{line}</span>
    }
</div>
```

Mouse wheel, keyboard navigation, stick-to-bottom — todo incluido.

### 8. Timers para spinner

```go
tui.OnTimer(time.Second, s.tick)  // interval timer
```

Spinner = `State[int]` de frame index + `OnTimer` cada 80ms.

---

## Mapping de eventos agent → go-tui

| `agent.Event` | go-tui |
|---|---|
| `EventTurnStart` | `spinning.Set(true)` |
| `EventTurnEnd` | `spinning.Set(false)` |
| `EventStreamTextDelta` | `streamWriter.Write(delta)` |
| `EventStreamThinkingDelta` | `streamWriter.WriteGradient(delta, thinkingGradient)` |
| `EventStreamTextEnd` | `streamWriter.Close()` |
| `EventStreamToolBuilding` | *(spinner ya activo)* |
| `EventToolCall` | `app.PrintAboveln("⚡ name args")` |
| `EventToolResult` | `app.PrintAboveln("  ✓ result [dur]")` |
| `EventTokens` | `footerState.Set(buildFooter(...))` |
| `EventError` | `app.PrintAboveln("✗ error msg")` |

---

## Lo que cambiaría en harness

### Se borra (915 líneas de terminal management complejo):

```
transport/cli/render.go      681 líneas  ← reemplazado por .gsx components
transport/cli/rawinput.go    100 líneas  ← reemplazado por <textarea>
transport/cli/clipboard.go   134 líneas  ← parcialmente (handler custom para images)
```

### Se reescribe (más pequeño):

```
transport/cli/cli.go    420 líneas → chat.gsx + main.go (estimado ~200 líneas total)
transport/cli/colors.go 157 líneas → clases Tailwind-style en .gsx (muchas se eliminan)
```

### Nueva dependencia:

```
github.com/grindlemire/go-tui  (solo golang.org/x/{sys,tools})
```

Reemplaza `charmbracelet/x/term`.

### Nuevo paso de build:

```bash
go install github.com/grindlemire/go-tui/cmd/tui@latest
tui generate ./transport/...   # compila .gsx → _gsx.go
go build .
```

Los `*_gsx.go` generados se commitean al repo — no se necesita el CLI en CI si se commitean.

### Lo que NO cambia (zero touch):

```
agent/         ← zero cambios
llm/           ← zero cambios
config/        ← zero cambios
agent/tools/   ← zero cambios
```

La separación backend/frontend paga aquí. Solo `transport/` cambia.

---

## El ejemplo `ai-chat` de go-tui es casi harness

El ejemplo oficial es un AI chat que llama al CLI de Claude Code.
La estructura es **idéntica** a la nuestra — solo que ellos ejecutan un proceso externo
y nosotros usamos nuestra propia LLM stack.

```go
// Ellos:
cmd := exec.Command("claude", args...)
stdout, _ := cmd.StdoutPipe()
go func() {
    scanner := bufio.NewScanner(stdout)
    for scanner.Scan() {
        c.eventCh <- parseEvent(scanner.Bytes())
    }
    c.eventCh <- streamEvent{Type: eventDone}
}()

// Nosotros:
go func() {
    a.Chat(ctx, userID, text, images)  // emite agent.Events al renderer
    eventCh <- agent.Event{Type: agent.EventTurnEnd}
}()
```

---

## Plan de implementación

### Fase 1: Prototipo en paralelo (1 sesión)

- `go get github.com/grindlemire/go-tui`
- Crear `transport/tui/` en paralelo a `transport/cli/` (sin borrar el viejo)
- `transport/tui/chat.gsx`: textarea input + ChannelWatcher + StreamWriter
- Conectar `agent.Chat()` via goroutine que bombea eventos a un canal
- Verificar que streaming funciona token a token

### Fase 2: Feature parity (2-3 sesiones)

- Spinner (label por turno con `tui.OnTimer` + `State[string]`)
- Footer reactivo (`State[string]` con tokens/costo/modelo/thinking)
- Commands: `/model`, `/thinking`, `/clear`, `/help`, `/exit`
- Thinking blocks con gray gradient vs cyan gradient para text
- Image paste (Ctrl+V → custom key handler → `LoadImage`)
- Banner con ASCII art

### Fase 3: Cutover

- Borrar `transport/cli/`
- Actualizar `main.go` para usar `transport/tui/`
- Actualizar `go.mod`
- Actualizar `AGENTS.md` con nueva estructura
- Actualizar CHANGELOG + versión

---

## Estructura final esperada

```
transport/
└── tui/
    ├── main.go          ← tui.NewApp + wiring
    ├── chat.gsx         ← componente principal (input + streaming)
    ├── chat_gsx.go      ← generado por tui generate (commiteable)
    ├── message.gsx      ← componente de mensaje (thinking/text/tool)
    ├── message_gsx.go   ← generado
    ├── footer.gsx       ← footer reactivo
    ├── footer_gsx.go    ← generado
    └── colors.go        ← gradients y constantes de color
```

---

## Dependencias del prototipo

```go
// go.mod additions:
require (
    github.com/grindlemire/go-tui v0.x.x  // pin version
)
```

Verificar que `golang.org/x/sys` ya en `go.sum` no conflictúa con versión actual
(actualmente tenemos `v0.38.0`, go-tui usa `v0.40.0` — minor bump, compatible).

---

## Referencias

- Docs completas: https://go-tui.dev/llms-full.txt
- ai-chat example: https://github.com/grindlemire/go-tui/tree/main/examples/ai-chat
- streaming example: https://github.com/grindlemire/go-tui/tree/main/examples/16-streaming
- inline-streaming example: https://github.com/grindlemire/go-tui/tree/main/examples/17-inline-streaming
- inline-mode guide: https://go-tui.dev/guide/inline-mode
- watchers guide: https://go-tui.dev/guide/watchers
