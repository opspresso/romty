# romty

romty는 Root Folder 아래의 workspace와 persistent terminal tab을 관리하는 로컬 TUI다. 왼쪽에는 Root와 workspace를, 오른쪽에는 terminal tab과 현재 terminal 화면을 항상 함께 표시한다. tmux식 split pane, prefix key, copy mode는 만들지 않는다.

## 요구 사항

- macOS 또는 Linux
- Go 1.25 이상

## 설치

```sh
go install ./cmd/romty
```

또는 저장소 안에서 binary를 만든다.

```sh
go build -o build/romty ./cmd/romty
```

## 사용

```sh
romty
```

처음 실행하면 background daemon을 자동으로 시작한다. `a`를 눌러 Root Folder 경로를 입력한다. Root 아래의 1-depth directory가 왼쪽에 workspace 후보로 나타난다.

왼쪽 pane의 현재 탐색 항목은 green으로 표시한다. 각 workspace 이름 뒤의 `●` 개수는 실행 중인 terminal tab 수를 나타낸다.

| 키 | 동작 |
|---|---|
| `a` | Root Folder 등록 |
| `↑`/`↓`, `j`/`k` | Root와 workspace 이동 |
| `Tab` | 선택된 terminal에 focus |
| `←`/`→`, `h`/`l` | navigation 상태에서 terminal tab 이동 |
| `Enter` | workspace 선택 |
| `+` | 선택한 workspace에서 terminal 생성 후 focus |
| `r` | Root directory와 session 상태 새로고침 |
| `Ctrl+G`, click | 다음 한 번의 click으로 Workspace/Terminal focus 전환 |
| `Ctrl+\` | terminal focus에서 Root navigation으로 복귀 |
| `q` | navigation 상태에서 romty 종료 |

terminal focus에서는 `Ctrl+G`와 `Ctrl+\`를 제외한 key와 paste를 daemon이 소유한 PTY로 전달한다. PTY 출력은 VT emulator로 해석해 오른쪽 pane 안에 렌더링한다. 평상시에는 mouse tracking을 끄므로 표시된 텍스트는 일반 terminal의 mouse selection과 copy를 사용한다. `Ctrl+G`를 누르면 다음 click까지만 mouse tracking을 켜며, `Esc`로 취소할 수 있다.

romty terminal 안에서 `romty`를 다시 실행하는 중첩 TUI는 허용하지 않는다.

## 상태와 수명

Root, 선택된 Workspace, Tab metadata는 `os.UserConfigDir()/romty/state.json`에 저장한다. Unix socket과 daemon log도 같은 directory에 둔다. 테스트나 격리가 필요하면 `ROMTY_HOME`으로 directory를 바꿀 수 있다.

```sh
ROMTY_HOME=/tmp/romty-dev romty
```

dashboard를 종료하거나 terminal window를 닫아도 daemon과 실행 중인 PTY process는 유지된다. `romty`를 다시 실행하면 기존 Root, Workspace, Tab을 불러오고 살아 있는 terminal session에 다시 연결한다.

daemon 종료나 OS 재부팅 뒤에는 Root와 Workspace metadata만 복구한다. 다시 연결할 수 없는 이전 Tab metadata는 daemon 시작 시 제거한다. 실행 중인 shell이 종료된 Tab도 즉시 제거한다.

## 구조

```text
romty TUI
   ├─ Root / Workspace pane
   └─ Terminal tabs / VT pane
   │
Unix Socket
   │
romty daemon
   ├─ Root
   │   └─ Workspace
   │       ├─ PTY / Tab
   │       └─ PTY / Tab
   └─ ...
```

daemon은 PTY 출력을 계속 읽고 각 session별 최근 8 MiB를 보관한다. 재연결할 때 이 출력을 client의 VT emulator에 replay해 terminal 화면을 복원한다. Remote/SSH 기능은 제공하지 않는다.
