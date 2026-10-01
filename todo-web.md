# Builda Web UI 개편 — 구현 결과와 후속 검증

작성: 2026-10-02 · 검토 기준 HEAD: `3a70500` · 저장소: `/home/lacti/git/dooroo/builda`

## 0. 구현 결과와 지침

앞 세션에서 실제 Chrome 리뷰로 P0 가독성·체크박스·모바일 overflow 문제와 실행 목록·로그·관리 폼 문제를 기록했다. 이 세션은 단계 A–E 구현을 마치고 결과와 제한을 이 파일에 반영했다. UI 작업은 Codex가 직접 했고 commit/push/deploy는 수행하지 않았다.

필수 문맥:

- `rules/architecture.md`: 단일 중앙 큐, 실행 상태, 복구, Astro 정적 UI, 로그 DOM 보존, API 허용 필드.
- `rules/security.md`: 인증·CSRF·일회성 토큰·특권 스크립트·출력 이스케이프.
- `rules/testing.md`, `rules/workflow.md`: 실제 브라우저 검증, Go 검사, dist 커밋 대상, 소스 파일 500줄 제한.
- `DESIGN.md`: 실제 운영 콘솔의 토큰, 타이포, 내비게이션, 스크롤 기준.
- `/home/lacti/.codex/skills/frontend-skill/SKILL.md`: Apps와 Utility Copy 지침을 적용. 랜딩 페이지 hero, 사진, 장식적 애니메이션은 이 제품에 불필요하다.
- 사용자는 Codex 직접 작업을 원한다. 위임 없이 진행한다.

## 1. 직접 브라우저 검토의 범위와 근거

이 절은 구현 전 기준선 검토 기록이다. 실제 임시 controller 로그인·CRUD 검증과 구현 후 화면 캡처는 7절에 기록했다.

Google Chrome을 Python Playwright로 구동하여 현재 `web/src`를 Astro 개발 서버 `http://127.0.0.1:4329`에서 렌더링했다. 실제 CSS/JS/번역/이벤트를 사용했고 API만 브라우저 라우팅으로 합성 응답했다. 운영 서버·실제 자격 증명·실제 빌드·상태 변경은 사용하지 않았다. 따라서 아래는 실제 브라우저 렌더링과 클라이언트 동작의 근거이며, 서버 인증·CRUD·실제 에이전트 연동의 통과 증거는 아니다.

검토 조합: 1440×900 light/ko, dark/ko; 390×844 light/ko, dark/ko; 320×740 dark/en; 768×1024 light/en. 작업, 작업 편집, 실행 모달, 큐, 실행 기록, 에이전트, 카탈로그, 설정, 로그인 및 실행 상세를 캡처했다. 실행 상세 `/runs/audit-run-000`는 Astro `/run` HTML을 브라우저 라우트로 매핑했다. Go의 `controllerPageFile`가 운영에서 하는 동일한 경로 매핑이므로 `/run`을 제품 링크로 만들지 말 것.

합성 데이터: 긴 한국어 작업 이름, 영문 ID, 두 파라미터, 에이전트, 카탈로그, 실행 20개(8개 상태 반복), 로그 120줄. 빈 목록·수천 개 목록·실제 네트워크 지연은 후속 검증이 필요하다.

로컬 증거는 `/tmp/builda-web-audit-20261002/`에 있다:

- `audit.py`: 재실행 가능한 브라우저 검토 스크립트. `python /tmp/builda-web-audit-20261002/audit.py` (먼저 dev 서버 실행).
- `measurements.json`: viewport, 문서 너비, 헤더 높이, computed style, Escape 결과, 폼 초안 결과.
- `{page}-{width}-{theme}-{locale}.png`: 페이지별 full-page 이미지. `job-editor-*`, `run-modal-*`도 있음.
- 대표: `runs-1440-dark-ko.png`, `runs-1440-light-ko.png`, `job-editor-390-dark-ko.png`, `queue-390-light-ko.png`, `runs-390-dark-ko.png`, `login-390-dark-ko.png`.

/tmp는 삭제될 수 있다. 문서의 수치와 재현 절차를 독립적인 근거로 사용하고 새 세션에서 캡처를 다시 만든다. 긴 full-page 이미지는 축소 표시되므로 1:1 viewport 캡처도 추가할 것. 개발 서버의 Astro 디버그 툴바는 제품 UI가 아니며 배포판 리뷰에서는 제외한다.

## 2. 구현 전 적대적 리뷰: 초기 화면이 실패한 이유

### P0 — 핵심 내용을 읽거나 정상 조작할 수 없는 결함

**F01. 실행 기록의 글자가 배경에 사라짐.** `components.css`의 전역 `button`은 `display:inline-flex`, 전경색 `--canvas`, 배경 `--fg`를 적용한다. `runs.js`는 `button.run`을 렌더링하지만 `.run`은 배경만 `--canvas`로 바꾸고 전경색을 바로잡지 않는다. 비선택 행의 실행 ID는 light에서 흰색/흰색, dark에서 `#0d1117`/`#0d1117`이다. 선택 행은 `button.active`로 배경이 반전되는데 `.run-name`은 `--fg`를 유지하여 제목이 배경과 동일해진다. 대비는 1:1. `.run-item.active` 규칙은 실제 `.run.active`에 적용되지 않는다. 단순 팔레트 취향 문제가 아니라 정보를 지우는 CSS 결함이다.

**F02. 실행 기록 한 행이 가로 flex로 압착됨.** `.run`은 목록 행 레이아웃을 정의하지 않아 제목·ID·칩·시간이 전역 버튼의 가로 flex 자식으로 경쟁한다. 1440px에서도 406px 행 안의 제목 폭이 약 74px, 높이 87px가 되어 6줄로 잘린다. `overflow-wrap:anywhere`는 이 구조 결함을 가린다. 제목, 상태, 메타, 파라미터, 시간 순의 명시적 행 grid가 필요하다. 전역 버튼은 기능 버튼만 스타일링하고 클릭 가능한 행에는 별도 컴포넌트 스타일을 적용한다.

**F03. 체크박스가 거대한 입력 상자가 됨.** 전역 `input`의 width 100%, min-height 38px, padding 8px 10px, shadow가 checkbox에 적용된다. 큐 선택은 큰 빈 직사각형이고 작업 편집의 사용 여부도 넓은 상자로 펼쳐진다. 브라우저 측정에서 작업 폼 체크박스 폭은 데스크톱 약 1356px였다. 텍스트 계열 입력과 checkbox/radio의 스타일을 분리한다.

**F04. 모바일 내비게이션이 화면과 독해를 지배함.** 390px 한국어 화면의 sticky 헤더가 240px(844px 높이의 약 28%)를 점유하고 실행 기록/카탈로그/에이전트가 글자 단위로 세로 줄바꿈된다. 브랜드, 메뉴 5개, 환경 설정, 사용자, 설정, 로그아웃, 빌드 ID가 모두 경쟁한다. 320px 영어에서는 문서 scrollWidth가 402px로 실제 가로 넘침이 생긴다. `.primary-nav`의 flex 압축과 `.nav-link`의 최소 폭 부재가 원인 중 하나다. 모바일 헤더는 한 줄, 메뉴는 명시적인 접이식 내비게이션으로 바꾼다. `overflow-x:hidden`으로 문제를 숨기지 않는다.

### P1 — 흐름을 방해하거나 정보를 잃게 하는 결함

**F05. CSS 색상 어휘가 두 벌이고 한 벌은 미정의.** `management.css`가 `--surface`, `--surface-strong`, `--text`를 사용하지만 정의는 `--canvas`, `--fg`뿐이다. computed style에서 앞의 세 값은 빈 문자열이고 record/input 배경이 transparent가 된다. 상속 때문에 모든 글자가 사라지는 것은 아니지만 관리 화면·로그인·폼 표면 위계가 무너진다. dark/system 선언도 복제되어 변경 누락 위험이 있다. semantic 토큰을 하나로 통일한다.

**F06. 관리 폼·필터에 바깥 여백이 없음.** `.panel` 안의 `#job-form`, `#agent-form`, `#catalog-form`, `#token-form`, `#run-filters`에 공통 body padding이 없다. 라벨·입력이 panel 경계에 붙고 header만 16px로 들여써져 정렬이 끊긴다. 반면 `.summary` 16px 안에 `.summary-head` 16px가 중복되는 등 같은 계층의 인셋이 다르다. heading-body-footer의 인셋을 한 시스템으로 정리한다.

**F07. 모바일 실행 기록에서 로그에 도달하기 어려움.** 960px 이하에서 두 열이 한 열이 되지만 20개 실행 목록이 모두 요약/로그 앞에 놓인다. `.responsive-picker` CSS는 있지만 현재 페이지는 picker를 렌더링하지 않고 목록에도 해당 클래스가 없다. 실행 선택 이후에도 로그가 수 화면 아래에 있다. 작은 화면에서는 목록→상세 경로 또는 별도 보기 상태를 사용해 선택 즉시 상세로 이동하고 뒤로가기로 목록과 필터/위치를 복원한다.

**F08. 스크롤 정책이 모순됨.** `pre` min-height 520px, max-height `calc(100vh - 410px)`는 900px 화면에서도 최대값 490px보다 최소값이 커서 최소값이 우선한다. 낮은 화면은 더 나쁘다. body + 로그 overflow + sticky inspector가 혼합되어 포인터 위치에 따라 휠의 대상이 달라진다. 모바일은 max-height를 없애 120줄이 긴 문서로 펼쳐진다. 콘텐츠에 따라 늘어난 헤더를 sticky inspector의 고정 64px 계산이 반영하지 못한다. body와 내부 스크롤의 소유자를 명시해야 한다.

**F09. textarea의 전역 620px 최소 높이.** 작업 스크립트의 `rows=10`도 무시하고 엄청난 빈 공간이 생긴다. 폼의 파라미터와 저장 버튼을 밀어낸다. YAML과 작업 스크립트는 서로 다른 크기·확장 정책이 필요하다. textarea와 문서 스크롤을 사용자가 예측할 수 있게 한다.

**F10. 실행 모달은 dialog 표기만 있고 동작이 불완전.** Escape로 닫히지 않는 것을 브라우저에서 재현했다. `jobs.js`에는 focus trap, 배경 inert, body scroll lock, 닫을 때 트리거로 focus 복원도 없다. 키보드 사용자는 배경으로 빠져나갈 수 있다(정적 코드 근거; 전체 Tab 경로 후속 검증). 실패 알림이 모달 밖 페이지 위의 notice로 전달되어 실패 이유를 못 볼 수 있다. 모달 내 오류·재시도·중복 제출 방지가 필요하다.

**F11. 반복 폼을 조작하면 작성한 초안이 소실됨.** 작업 파라미터를 추가하고 ID에 `draft-kept`를 입력한 뒤 다시 추가하면 첫 ID가 빈 문자열로 바뀌는 것을 6개 브라우저 조합 모두에서 재현했다(`parameter-draft` 측정). 작업 파라미터/카탈로그 옵션은 draft 배열로 다시 렌더링하지만 현재 DOM 값을 배열로 수집하는 이벤트가 없다. 카탈로그도 같은 코드 패턴이므로 별도 회귀 검증한다. 디자인 개편 시 이 동작 결함을 함께 해결해야 한다.

**F12. 폴링이 목록/요약 DOM을 통째로 교체함.** runs/queue는 2초, agents는 5초. focus, 열린 details, 메뉴, 선택 텍스트를 잃을 수 있다. 로그만 안정성 가드가 있고 나머지에는 없다. 목록 버튼의 focus 보존 및 팝오버 유지 여부는 후속 키보드 검증이 필요하다. 사용자 조작 중 백그라운드 업데이트가 화면을 흔들면 안 된다.

### P2 — 정보 구조·시각 언어·운영 판단의 결함

**F13. 박스 안에 박스와 중복 제목.** 작업 페이지의 eyebrow/제목/설명/패널 제목이 반복된다. record, chip, 입력, nav에 둥근 테두리·shadow가 겹친다. 상태나 실행이 아니라 chrome이 주의를 가져간다. 운영 UI는 단정한 섹션과 행, 분리선 위주로 재구성한다.

**F14. 텍스트 위계가 너무 작고 코드 스타일이 남용됨.** body 14px, meta/시간/칩 12px, 버튼 13px. 설명과 날짜까지 monospace라 한국어 리듬이 끊기고 줄바꿈이 늘어난다. 기본 정보는 sans, 코드·ID·경로·로그에만 mono. muted/faint는 실제 표면 기준 대비를 측정한다. 현재 모든 muted 값이 실패한다고 단정하지는 않는다.

**F15. 파괴적/관리 버튼의 과밀.** 작업마다 실행·편집·사용중지·기록·삭제가 나란하고 에이전트는 더 많다. 실행 버튼이 주 동작인 화면에서 삭제와 토큰 회전까지 동등하게 노출되어 인지 부담이 크다. 자주 사용하는 동작 1–2개를 남기고 나머지는 접근 가능한 메뉴/관리 상세로 이동한다.

**F16. 운영 상태를 읽는 비용이 큼.** noAgent 경고가 그냥 hint, blocked는 문맥 부족, ONLINE/OFFLINE/paused/disabled/busy의 위계가 약하다. 큐 대기 이유는 칩 하나이고 어떻게 해소할지 연결이 없다. API가 제공한 사실을 표시하고 관련 관리 화면으로 연결하되, 대기순번을 확정 실행순서나 시작 예정 시간처럼 표현하지 않는다.

**F17. 상태 의미 누락/혼동.** `listing.css`에는 ASSIGNED/CANCELING의 별도 규칙이 없다. CANCELED/ABORTED/FAILED가 같은 위험색이다. 텍스트로 구분하되 실패·사용자 취소·중단·취소 처리 중의 운영 의미를 함께 정리한다. 색만으로 상태를 전달하지 않는다.

**F18. 오류·로딩·빈 화면·새로고침 상태 부족.** 공통 none는 무엇이 비었는지, 왜, 다음 행동이 무엇인지 알리지 않는다. 로딩과 진짜 빈 목록, 실패 후 이전 데이터가 남은 상태를 구별해야 한다. 총 200개 조회와 total을 표시하지만 페이지 탐색은 없어 과거 기록 접근이 제한된다. API의 지원 범위를 확인해 페이지 이동을 제공한다.

**F19. 문자열/접근성 일관성.** `20 runs`, `busy`, Follow 등의 노출과 번역을 정리한다. 현재 nav는 active CSS만 있고 `aria-current`는 없다. 초점·hover·선택·disabled·loading을 각각 정의한다. 전역 hover translateY는 정보 행과 헤더를 불필요하게 흔든다. reduced-motion을 존중한다.

## 3. 개편 방향 — 구현자가 임의로 다시 설계하지 않도록

시각 논제: 명확한 글자 대비와 정렬을 갖춘 현대적인 빌드 운영 콘솔. 낮은 장식 밀도, 충분한 작업 공간, 하나의 절제된 강조색, 읽기 쉬운 정보 밀도.

내용 계획: 작업 선택과 실행 → 큐 상태 확인 → 실행 결과와 로그 조사. 카탈로그·에이전트·설정은 관리 맥락으로 분리하되 어느 화면에서나 접근할 수 있다. 첫 화면에 마케팅 hero나 통계 카드 모자이크를 추가하지 않는다.

인터랙션 논제: 선택 상태와 focus는 즉시 명료하게, 메뉴/모달 전환은 120–180ms 정도의 짧은 변화만 사용. 폴링은 조용하게, 사용자 스크롤과 초안을 보존. 장식적 모션은 없다.

### 토큰과 치수의 시작 사양

아래는 구현된 토큰이다. system preference를 첫 paint 전에 `data-color-scheme`으로 해석하며 dark palette는 light palette의 단순 반전이 아니다.

| 역할 | Light | Dark |
| --- | --- | --- |
| 앱 배경 | #f6f8fa | #010409 |
| 기본 표면 | #ffffff | #0d1117 |
| 보조 표면 | #f0f3f6 | #161b22 |
| 주 텍스트 | #1f2328 | #e6edf3 |
| 보조 텍스트 | #59636e | #8b949e |
| 기본 구분선 | #d0d7de | #30363d |
| 조작 경계 | #838e9b | #697584 |
| 주요 액션 | #0969da | #58a6ff |
| 선택 배경 | #ddf4ff | #0d2d4d |

- 버튼 배경과 버튼 글자는 별도 토큰. active 행은 표면 반전 대신 선택 배경 + 명확한 표시를 우선한다.
- 일반 글자 최소 대비 4.5:1, 큰 글자 3:1, 조작 경계·focus/선택 표시 3:1을 합격 기준으로 측정. opacity와 실제 상위 배경까지 합성해서 계산한다.
- 간격 스케일 4/8/12/16/24/32. desktop content gutter 24–32px, mobile 16px(320px는 12px 허용). panel body desktop 20–24px/mobile 16px.
- 본문 desktop 15px/mobile 16px 시작; 보조 정보 13–14px; 주요 제목 24–28px; 섹션 18–20px; line-height 1.45–1.6. 입력은 모바일 16px 이상으로 iOS 확대 방지.
- 980px 이하 터치 영역 최소 44×44px. 체크박스 시각 크기 19px, label 영역으로 44px 확보. 본문 입력은 모바일 16px.
- ID/경로는 줄바꿈·축약·전체값 확인/복사를 함께 제공. 제목은 단어 단위 자연 줄바꿈, 데스크톱 행 제목 최대 2줄 권장. 값이 잘려도 복사/접근 경로가 있어야 한다.
- 로그인은 최대 400–440px, 일반 폼은 약 880–1040px; 실행/로그 작업 공간은 넓게. 1440px max-width를 모든 페이지에 일괄 강제하지 않는다.

### 레이아웃과 스크롤의 결정

- 넓은 화면: 브랜드와 간결한 주 내비게이션, 계정 메뉴. 사용자/빌드 버전/테마/언어/로그아웃은 계정·환경 메뉴나 설정에서 접근. 운영 메뉴와 관리 메뉴를 시각적으로 구분한다.
- 좁은 화면: 약 56–64px 단일 헤더 + 메뉴 버튼. 열린 메뉴는 현재 위치 표시, focus 관리, Escape, 명확한 닫기. 전체 nav 텍스트를 한 줄에 압축하지 않는다.
- 기록 데스크톱: 목록 폭 약 360–440px, 나머지 로그 상세. 목록 행 내부는 제목/상태, ID·에이전트, 요약 파라미터, 주요 시간의 세로 계층. 카드마다 모든 시간을 펼치지 않는다.
- 기록 모바일/좁은 tablet: 목록과 상세를 동시에 길게 쌓지 않는다. `/runs/{id}` 상세로 이동하고 복귀 시 필터/목록 위치 복원. 작은 화면에서 기존 상세 URL을 재사용하는 방향을 우선한다.
- 일반 관리 화면은 문서 하나의 세로 스크롤. 전역 `body overflow:hidden`은 금지.
- 로그 화면만 명시적 읽기 영역에 내부 세로 스크롤 하나를 허용한다. 기본 높이는 viewport에 맞게 clamp하고 헤더·요약은 압축/접기 가능하게 한다. min-height가 max-height를 넘지 않아야 한다. 짧은 높이에서도 로그/버튼에 도달 가능해야 한다.
- 로그 내부 휠/터치는 로그를 움직이고 바깥은 문서를 움직이도록 정책을 정의; overscroll 전파도 직접 검증. 모바일은 전체 로그 보기 또는 bounded viewer로 조작 가능하게 한다.
- 로그 줄바꿈 토글과 원문 복사를 제공. 긴 한 줄의 가로 스크롤은 로그 안에서만 허용. 문서 전체 가로 스크롤은 금지.
- Follow는 사용자가 과거 로그로 스크롤하면 멈추고, 명시적으로 재개하면 끝으로 이동. 신규 로그 안내를 작게 제공. polling이 문서를 이동시키거나 선택을 지우지 않아야 한다.

## 4. 구현 순서와 상세 TODO

각 단계가 끝날 때 해당 브라우저 검증을 먼저 통과시킨다. 새 기능 라이브러리/프레임워크 교체는 필요하지 않다.

### 단계 A — P0 복구와 공통 기반

- [x] `components.css`: 전역 input/checkbox/radio 분리; 기능 버튼과 행 버튼의 레이아웃·전경·배경·line-height 분리. F01–03 해결.
- [x] `runs.js` + `components.css`/`workspace.css`: `.run` 실제 markup과 CSS를 일치시키고 사용하지 않는 `.run-item`/picker 규칙을 제거했다.
- [x] `base.css`, `management.css`: semantic 토큰을 통일하고 미정의 변수를 제거했다. system 테마는 첫 렌더 전에 OS 테마로 풀고 light/dark 값을 한 군데서 관리한다.
- [x] 공통 section body/footer 인셋과 form spacing을 정의하고 작업·카탈로그·에이전트·설정·실행 기록 화면에 적용했다.
- [x] textarea를 스크립트/YAML 용도에 맞게 분리하고 전역 620px 최소 높이를 제거했다. 낮은 높이와 모바일에서 스크롤을 확인했다.
- [x] focus-visible, 선택 행, disabled, pending, danger, 링크를 정의했다. 8개 실행 상태와 에이전트 상태는 텍스트와 색으로 구분한다.
- [x] 1440px light/dark에서 선택/비선택 제목·ID, 체크박스, 입력 대비를 브라우저에서 확인했다.

### 단계 B — 셸과 모바일

- [x] 모바일 접이식 내비게이션과 계정/환경 메뉴를 구현하고 `builda.theme`, `builda.locale` 저장 키를 유지했다.
- [x] `aria-current`, skip link, expanded 상태, Escape/바깥 클릭, 초점 복원 동작을 구현했다.
- [x] build ID 전체 값은 계정 메뉴에서 확인하도록 옮겼고 로그아웃을 유지했다.
- [x] 320/360/390/430/768/1024/1280×720/1440/1920px와 모바일 가로·720×450을 포함한 96개 경로/폭 조합에서 문서 가로 넘침과 page error가 없었다.
- [x] sticky header 높이를 breakpoint별 토큰으로 쓰고 문서 scroll padding을 적용했다.

### 단계 C — 실행 기록·상세·로그

- [x] 데스크톱은 폭 360–440px 목록과 로그 inspector를 나란히 둔다. 좁은 화면은 `/runs/{id}` 상세 경로로 이동하고 돌아올 때 필터·선택·스크롤을 복원한다.
- [x] 필터, 재설정, total, API limit/offset 페이지 이동을 연결했다.
- [x] bounded log, 줄바꿈, Follow 중단/재개, 신규 출력 안내, 원문 복사 성공/오류 상태를 구현했다.
- [x] 로그는 새 출력만 append하고 기존 줄을 유지한다. 실제 브라우저에서 텍스트 선택이 polling 뒤에도 남는 것을 확인했다.
- [x] request generation과 중복 요청 억제로 느린 이전 실행/필터 응답이 새 선택을 덮지 않게 했다. A→B 지연 응답을 브라우저에서 확인했다.
- [x] 동일한 목록/요약은 다시 그리지 않고 focus·열린 script details·스크롤을 보존한다. 숨겨진 탭의 polling은 멈춘다.
- [x] 상세 요약을 agent, timeout, requested/finished, parameter의 읽기 쉬운 구조로 정리했다.
- [x] active run은 취소하고 terminal run은 삭제한다. CANCELING은 취소 진행 중으로 남기며 반복 취소를 숨긴다.

### 단계 D — 작업·큐·에이전트·카탈로그·설정

- [x] 작업명·설명·실행 동작을 앞세우고 자주 쓰지 않는 관리 동작을 disclosure 메뉴로 옮겼다. 샘플 controller에는 작업 2개가 있어 검색은 추가하지 않았다.
- [x] 실행 모달에 focus trap/return, Escape, inert 배경, scroll lock, 내부 오류와 중복 제출 방지를 구현하고 실패 후 재시도를 확인했다.
- [x] 실행 폼의 required 속성을 API 설정과 맞추고, 브라우저 제약 검증과 API 오류 표시를 확인했다.
- [x] parameter/option repeater는 DOM 값을 모은 뒤 다시 그리며, 가운데 항목 삭제와 추가 후 값·focus를 보존한다. 한국어/영어 label은 현재 폼에서도 갱신된다.
- [x] 큐 선택 개수와 batch별 결과를 표시하고, reason/eligible agents를 설명한다. queued와 active는 구역을 나누고 checkbox 키보드 조작을 확인했다.
- [x] 에이전트 상태와 현재 실행을 보이고, pause/token/delete를 메뉴에 둔다. 토큰 회전과 삭제 확인을 추가했다.
- [x] 카탈로그에 옵션 수와 참조 작업을 표시하고 긴 값을 줄바꿈해 repeater 여백을 정리했다.
- [x] API token 관리와 YAML editor를 구분했다. YAML의 수정/저장/오류 상태를 보이고 미저장 편집을 다시 불러올 때 확인한다.
- [x] 일회성 token은 안전하게 출력/복사하고 pagehide 때 DOM에서 지운다. 통합 검증은 임시 토큰만 사용했고 캡처하지 않았다.
- [x] 로그인 오류/pending/autocomplete를 유지하고 모바일 입력 크기를 16px로 맞췄다. 인증 구조는 변경하지 않았다.
- [x] 새 UI 문구를 ko/en에 추가하고 count/Follow/busy/empty 화면을 번역했다.

### 단계 E — 검증·문서·산출물

- [x] `DESIGN.md`를 실제 운영 앱의 토큰/타이포/간격/내비게이션/스크롤 원칙으로 갱신했다.
- [x] CSS/JS 파일을 500줄 미만으로 유지하고 기존 규칙을 고쳤다. `rules/architecture.md`에 바깥 클릭 처리의 DOM 교체 교훈을 추가했다.
- [x] frozen install과 frontend build를 실행하고 연속 빌드의 산출물/chunk 이름이 결정적으로 유지되는 것을 확인했다.
- [x] 임시 controller와 실제 browser에서 login/CSRF/CRUD/run/queue/history/cancel/log/detail/session expiry를 확인했다. 합성 `echo` 작업만 큐에 넣었고 운영 daemon은 건드리지 않았다.
- [x] `go test ./...`, `go test -race ./...`, `git diff --check`가 통과했다.
- [x] gitleaks가 50 commits를 검사해 leak 없음으로 종료했다.
- [x] 이 파일에 구현 결과, 브라우저 환경, 캡처 위치, 남은 검증 한계를 기록했다.

## 5. 브라우저 합격 기준과 시나리오

| 영역 | 합격 기준 |
| --- | --- |
| 테마 | light/dark/system × OS light/dark; 첫 렌더와 토글 때 readable; 선택/hover/focus/disabled/pending 모두 점검 |
| 화면 | 320/360/390/430/768/1024/1440/1920; 짧은 desktop 1280×720, mobile landscape; 문서 scrollWidth ≤ clientWidth(+1px 오차) |
| 언어/확대 | ko/en 각각 긴 제목·ID·경로·버전; 200% 확대에서 내용과 조작 접근 가능; 모바일 입력 16px 이상 |
| 가독성 | 실제 전경/배경 대비 수치 기록; 선택 행 제목/메타 모두 식별; 좁은 코드 열 때문에 한국어 이름이 한 글자씩 끊기지 않음 |
| 스크롤 | 문서/로그/모달에서 휠·트랙패드·터치, 끝 경계; 화면 점프나 header 가림 없음; 모바일 선택 후 여러 목록 화면을 내려야 로그가 보이는 구조 없음 |
| 키보드 | Tab/Shift-Tab/Enter/Space/Escape; 메뉴/모달 focus 포함·복원; 배경 focus 차단; 현재 위치와 focus 가시성 |
| 데이터 | 0/1/20/200개와 조회 제한 초과 total, 매우 긴 값, 상태 8종, blocked/offline/no eligible agent, 지연/오류/세션 만료 |
| 초안 | 파라미터 3개 입력→가운데 삭제→추가, 카탈로그 옵션도 동일; 기존 입력·focus 유지; 실패 후 재입력 강요 없음 |
| 폴링 | 2회 이상 refresh를 기다려 focus/details/선택 텍스트 유지; 느린 A 응답 뒤 B 선택을 덮지 않음 |
| 로그 | 긴 줄/120줄/새 출력/완료 후 정지; 텍스트 선택 후 polling/언어 변경; 위로 스크롤 시 Follow 정지; copy는 시각 포장 없는 원문 |
| 기능 | 실행/재실행/취소/취소 진행/삭제/토큰 회전·폐기/폼 저장; 중복 클릭과 실패 재시도 결과 명확 |

캡처는 viewport 크기로 before/after를 비교하고 긴 화면은 필요한 구역을 별도로 캡처한다. CSS assertion만 통과하고 눈으로 못 읽는 화면은 불합격이다. 실제 controller 검증과 합성 API 검증은 결과에 구분해서 기록한다.

## 6. 절대 지켜야 할 가드레일

- Go 단일 바이너리 + Astro 정적 frontend 유지. Go는 `web/dist`만 embed한다. 런타임 Node/SSR, CDN 필수 자산, 대규모 UI 라이브러리로 갈아엎지 않는다.
- 모든 controller API와 로그는 인증 필요. CSRF/same-origin/401 login redirect/HttpOnly 세션 보존. 사용 편의 때문에 인증을 우회하지 않는다.
- 서버가 허용한 필드만 POST/PUT. 조회 응답의 runtime/eligible/resolved 필드를 그대로 보내지 않는다.
- 스크립트·이름·파라미터·로그·경로는 escape/textContent로 렌더링. raw HTML로 바꾸지 않는다. 클라이언트 실행 요청에는 스크립트를 넣지 않는다.
- 기존 상태와 단일 중앙 큐, 취소/복구/permit/로그 offset protocol은 UI 개편 대상이 아니다. UI가 낙관적으로 RUNNING/SUCCESS로 확정하지 않는다.
- incomplete run 재실행이나 불확실한 프로세스 kill을 자동화하지 않는다. attention resolve는 운영 판단이 필요한 기존 행위로 남긴다.
- 원문 로그 복사, 바이트 offset, 변경 없는 로그 DOM 및 번역 제외 규칙 유지. 화면 디자인 때문에 로그의 의미나 선택 안정성을 훼손하지 않는다.
- API/라우팅/데이터 구조를 바꿔야 한다면 필요성과 검증을 별도 기록. 단순 디자인 수정으로 controller 계약을 확장하지 않는다.
- unrelated 사용자 수정 보존. binaries/state/spool/credentials/logs/screenshots/node_modules 등 임시 자산은 Git에 넣지 않는다. `web/dist`는 예외로 커밋 대상이다.
- 끝났다는 판단은 실제 브라우저와 적절한 검사 결과로 한다. 이번 문서의 제안 팔레트/치수가 그대로 정답이라는 가정은 금지.

## 7. 구현 결과와 남은 한계

완료: P0 가독성·checkbox·mobile header 결함, semantic token, form spacing, responsive run history/detail/log, mobile navigation, repeater draft loss, modal keyboard/failure/pending, queue/agent/catalog/settings states를 수정했다. `DESIGN.md`와 관련 architecture rule, committed `web/dist`도 갱신했다. API schema와 run lifecycle은 변경하지 않았다.

검증: Chrome/Playwright에서 96개 route/viewport 조합에 가로 overflow와 page error가 없었다. 844×390·740×360 landscape, 1280×720 짧은 desktop, 720×450 유효 작업 폭도 통과했다. log append 중 selection 유지, stale A→B log 응답, Follow pause/new-line notice, repeater 추가/삭제, modal required/failure/retry/double-submit, history 250개 페이지 이동, queue batch 결과를 브라우저에서 확인했다. light/dark/system과 OS theme 변경, 8개 상태의 text 대비를 확인했다. status 글자 최소 대비는 light 4.52:1, dark 4.85:1, control border는 각각 3.33:1과 4.04:1이었다.

격리한 임시 controller에서는 실제 로그인을 통해 CSRF 거부, job/catalog/agent CRUD, 합성 `echo` run enqueue/cancel/delete, queue/history/detail, API token issue/revoke, YAML 수정/저장/재불러오기, logout/session expiry를 확인했다. 실제 agent는 연결하지 않아 shell script가 실행되지 않았다. `go test ./...`, `go test -race ./...`, frozen frontend build, `git diff --check`, gitleaks가 통과했다. frontend build를 연속 실행해 dist chunk 이름과 내용이 안정적인 것도 확인했다.

캡처: `/tmp/builda-web-audit-20261002/`의 `{page}-{width}-{theme}-{locale}.png`, `controller-*-viewport.png`, `measurements.json`. 제품 화면 리뷰에는 debug toolbar가 없는 임시 production-controller viewport 캡처를 사용한다.

남은 한계: 데스크톱 브라우저의 실제 200% zoom UI는 자동화하지 못했다. 대신 같은 유효 폭인 720px와 짧은 450px 높이에서 reflow·입력 크기를 확인했다. 설정 예제는 작업 2개여서 별도 job search를 넣지 않았다. 확대/보조기술을 포함한 전면 접근성 인증은 별도 검토가 필요하다. commit/push/deploy는 요청하지 않아 하지 않았다.
