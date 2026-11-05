# Bespoke-Nimble-9B API 직접 연결 가이드

이 문서는 **Agent Chat Desktop을 사용하지 않고**, 일반 PC에서 IP 주소·포트·API 키를 이용해 Bespoke-Nimble-9B 서버에 직접 요청하는 방법을 설명합니다.

Nimble 서버는 OpenAI 호환 채팅 API가 아니라 분류·판단용 HTTP API를 제공합니다. 채팅용 `/v1/chat/completions`가 아닌 상태 확인 `GET /health`와 판단 요청 `POST /decide`를 사용합니다.

## 준비할 정보

서버 운영자에게 다음 정보를 받으세요.

| 항목 | 예시 | 설명 |
|---|---|---|
| 서버 주소 | `http://192.168.1.50:12300` | PC에서 접근 가능한 서버의 LAN IP와 포트 |
| 포트 | `12300` | 서버의 `--port` 설정과 같아야 함 |
| API 키 | 운영자가 별도로 전달 | 서버의 `NIMBLE_API_KEY`와 같은 값 |
| 모델 ID | `Bespoke-Nimble-9B` | 서버가 로드한 모델 ID |

서버 주소의 IP는 예시입니다. 실제 DGX Spark 또는 Nimble 서버의 IP로 바꾸세요. PC와 서버는 같은 사설 네트워크나 서로 연결된 VPN에 있어야 합니다.

## 서버 운영자: API 실행

서버에 Nimble 모델과 `nimble_api.py`가 준비되어 있어야 합니다. 서버 터미널에서 Nimble 폴더로 이동하고 가상환경을 활성화한 다음 API를 실행합니다.

```bash
cd /path/to/nimble
source .venv/bin/activate
read -rsp "NIMBLE_API_KEY: " NIMBLE_API_KEY
printf '\n'
export NIMBLE_API_KEY
uvicorn nimble_api:app --host 0.0.0.0 --port 12300 --workers 1
```

키는 입력할 때 화면에 표시되지 않습니다. 실제 API 키를 스크립트, 문서, 채팅, URL 또는 소스 저장소에 기록하지 마세요. 다른 포트를 쓰면 서버 실행 옵션과 PC에서 사용하는 주소의 포트를 동일하게 바꾸세요.

모델 로딩이 끝나면 서버 터미널에 `Model loaded successfully.`가 표시됩니다. `--host 0.0.0.0`은 네트워크 연결을 받기 위한 서버 측 설정입니다. PC의 주소에는 `0.0.0.0` 대신 서버의 실제 LAN IP를 사용합니다.

## PC에서 네트워크 연결 확인

Windows PowerShell에서 포트가 열려 있는지 확인합니다.

```powershell
Test-NetConnection 192.168.1.50 -Port 12300
```

`TcpTestSucceeded`가 `True`여야 합니다. IP와 포트는 실제 서버 값으로 바꾸세요.

브라우저에서 상태 확인 주소를 엽니다.

```text
http://192.168.1.50:12300/health
```

정상 응답에는 `"status":"ok"`와 `"model":"Bespoke-Nimble-9B"`가 포함됩니다. 이 상태 확인 경로는 API 키 없이 응답합니다. 따라서 `/health`가 열리는 것만으로 API 키 인증이나 판단 요청까지 확인된 것은 아닙니다.

터미널에서도 확인할 수 있습니다.

```bash
curl http://192.168.1.50:12300/health
```

## 판단 요청 보내기

다음 Python 예제는 외부 패키지 없이 실행됩니다. API 키는 입력할 때 화면에 보이지 않으며 코드나 명령 기록에 넣지 않습니다. `<서버 IP>` 부분을 실제 값으로 바꾸세요.

```python
import getpass
import json
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

base_url = input("Nimble 서버 주소 (예: http://192.168.1.50:12300): ").strip().rstrip("/")
api_key = getpass.getpass("NIMBLE_API_KEY: ")

payload = {
    "context": "고객이 상품 대금이 두 번 청구되었다고 문의했습니다.",
    "schema": {
        "decision": {
            "type": "enum",
            "choices": ["billing", "technical"],
            "choice_descriptions": {
                "billing": "요금, 결제, 환불 관련 문의",
                "technical": "제품 사용이나 기술 지원 관련 문의"
            },
            "description": "요청을 담당할 업무 영역을 하나 선택하세요."
        }
    },
    "score_fields": []
}

request = Request(
    f"{base_url}/decide",
    data=json.dumps(payload, ensure_ascii=False).encode("utf-8"),
    headers={
        "Authorization": f"Bearer {api_key}",
        "Content-Type": "application/json"
    },
    method="POST"
)

try:
    with urlopen(request, timeout=180) as response:
        result = json.loads(response.read().decode("utf-8"))
    print(json.dumps(result, ensure_ascii=False, indent=2))
except HTTPError as error:
    detail = error.read().decode("utf-8", errors="replace")
    print(f"HTTP {error.code}: {detail}")
except URLError as error:
    print(f"서버에 연결하지 못했습니다: {error.reason}")
```

`context`에는 판단할 내용을 넣고, `schema.decision`에는 출력 형식과 선택지를 정의합니다. 이 예제처럼 `type`이 `enum`이면 모델은 `choices` 중 하나를 고릅니다. 성공 응답의 `result.output.decision`에서 선택 결과를 확인하고, `result.fields.decision`에 포함된 확률 정보가 있으면 함께 확인할 수 있습니다.

## 오류 해결

| 증상 | 확인할 내용 |
|---|---|
| 연결 시간 초과 또는 연결 거부 | 서버 IP·포트, 서버 실행 여부, 양쪽 PC가 연결된 네트워크, DGX Spark 방화벽을 확인합니다. 서버가 `--host 0.0.0.0`으로 실행 중인지 확인하세요. |
| `/health`가 404 | 해당 주소와 포트에서 Nimble API가 실행 중인지 확인합니다. 다른 서비스 주소를 입력했거나 서버가 다른 포트를 쓸 수 있습니다. |
| `POST /decide`가 401 | `Authorization: Bearer ...`에 넣은 키와 서버를 실행할 때 설정한 `NIMBLE_API_KEY`가 같은지 확인합니다. 키를 변경한 뒤에는 API 서버를 새 키로 다시 시작해야 합니다. |
| `/v1/chat/completions`가 404 | 이 서버는 채팅용 OpenAI 호환 API가 아닙니다. 판단 요청에는 `POST /decide`를 사용합니다. |
| 요청이 422 또는 500으로 실패 | 요청 본문이 JSON인지, `context`가 문자열이고 `schema`가 객체인지 확인한 뒤 서버 터미널의 오류 로그와 모델 로딩 상태를 확인합니다. |

## API 키와 네트워크 보안

현재 예시는 HTTP를 사용하므로 API 키가 네트워크 구간에서 암호화되지 않습니다. 신뢰할 수 있는 사설 LAN이나 VPN 안에서 사용하고, 라우터 포트 포워딩으로 API를 인터넷에 직접 공개하지 마세요. 방화벽 접근도 필요한 PC나 내부 네트워크로 제한하세요. 외부에서 연결해야 한다면 VPN 또는 TLS가 적용된 보호 경로를 먼저 구성하세요.
