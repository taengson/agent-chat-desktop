import {FormEvent, useCallback, useEffect, useState} from 'react';
import {App as ChatService} from '../bindings/github.com/taengson/agent-chat-desktop';
import type {BenchmarkSyncV2Invitation, BenchmarkSyncV2State} from '../bindings/github.com/taengson/agent-chat-desktop/models';

function formatTime(value: string): string {
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return value;
    return new Intl.DateTimeFormat('ko-KR', {
        month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit',
    }).format(date);
}

export default function BenchmarkSyncWorkspace() {
    const [state, setState] = useState<BenchmarkSyncV2State | null>(null);
    const [deviceName, setDeviceName] = useState('');
    const [publicHTTPSURL, setPublicHTTPSURL] = useState('');
    const [certificatePath, setCertificatePath] = useState('');
    const [privateKeyPath, setPrivateKeyPath] = useState('');
    const [listenAddress, setListenAddress] = useState(':8443');
    const [invitation, setInvitation] = useState<BenchmarkSyncV2Invitation | null>(null);
    const [pairingURL, setPairingURL] = useState('');
    const [pairingSecret, setPairingSecret] = useState('');
    const [busyAction, setBusyAction] = useState('');
    const [error, setError] = useState('');
    const [notice, setNotice] = useState('');

    const applyState = useCallback((next: BenchmarkSyncV2State) => {
        setState(next);
        setDeviceName(next.deviceName);
        setPublicHTTPSURL(next.publicHTTPSURL || '');
        setCertificatePath(next.tlsCertificatePath || '');
        setPrivateKeyPath(next.tlsPrivateKeyPath || '');
        setListenAddress(next.listenAddress || ':8443');
    }, []);

    const refresh = useCallback(async () => {
        try {
            const next = await ChatService.GetBenchmarkSyncV2State();
            applyState(next);
        } catch (reason) {
            setError(reason instanceof Error ? reason.message : String(reason));
        }
    }, [applyState]);

    useEffect(() => {
        void refresh();
    }, [refresh]);

    async function runAction(action: string, operation: () => Promise<BenchmarkSyncV2State>, successMessage: string) {
        if (busyAction) return;
        setBusyAction(action);
        setError('');
        setNotice('');
        try {
            const next = await operation();
            applyState(next);
            setNotice(successMessage);
        } catch (reason) {
            setError(reason instanceof Error ? reason.message : String(reason));
        } finally {
            setBusyAction('');
        }
    }

    function submitDeviceName(event: FormEvent<HTMLFormElement>) {
        event.preventDefault();
        void runAction('device-name', () => ChatService.UpdateBenchmarkSyncV2DeviceName(deviceName), '이 장치 이름을 저장했습니다.');
    }

    function submitDirectTLS(event: FormEvent<HTMLFormElement>) {
        event.preventDefault();
        void runAction('direct-tls', () => ChatService.ConfigureBenchmarkSyncV2DirectTLS(publicHTTPSURL, certificatePath, privateKeyPath, listenAddress), 'v2 HTTPS 수신 endpoint를 시작했습니다.');
    }

    async function createInvitation() {
        if (busyAction) return;
        setBusyAction('create-invitation');
        setError('');
        setNotice('');
        try {
            const next = await ChatService.CreateBenchmarkSyncV2PairingInvitation();
            setInvitation(next);
            setNotice('15분 동안 유효한 초대를 만들었습니다. 주소와 secret은 신뢰할 수 있는 방법으로만 전달하세요.');
        } catch (reason) {
            setError(reason instanceof Error ? reason.message : String(reason));
        } finally {
            setBusyAction('');
        }
    }

    async function startPairing(event: FormEvent<HTMLFormElement>) {
        event.preventDefault();
        if (busyAction) return;
        setBusyAction('start-pairing');
        setError('');
        setNotice('');
        try {
            const outgoing = await ChatService.StartBenchmarkSyncV2Pairing(pairingURL, pairingSecret);
            const next = await ChatService.GetBenchmarkSyncV2State();
            applyState(next);
            setNotice(`${outgoing.host.deviceName}에 연결 요청을 보냈습니다. 두 화면의 fingerprint를 비교한 뒤 상대 장치에서 승인하세요.`);
        } catch (reason) {
            setError(reason instanceof Error ? reason.message : String(reason));
        } finally {
            setBusyAction('');
        }
    }

    async function checkPairing(requestID: string) {
        if (busyAction) return;
        setBusyAction(`check-${requestID}`);
        setError('');
        setNotice('');
        try {
            const outgoing = await ChatService.CheckBenchmarkSyncV2Pairing(requestID);
            const next = await ChatService.GetBenchmarkSyncV2State();
            applyState(next);
            setNotice(outgoing.status === 'approved' ? '상대 장치가 승인했습니다. fingerprint가 일치하면 연결을 확정하세요.' : `연결 요청 상태: ${outgoing.status}`);
        } catch (reason) {
            setError(reason instanceof Error ? reason.message : String(reason));
        } finally {
            setBusyAction('');
        }
    }

    async function copy(value: string, label: string) {
        try {
            await navigator.clipboard.writeText(value);
            setNotice(`${label}을(를) 복사했습니다.`);
        } catch {
            setNotice(`${label}을(를) 선택해 복사해 주세요.`);
        }
    }

    const logs = state?.logs || [];
    return (
        <main className="benchmark-sync-workspace">
            <header className="benchmark-sync-header">
                <div>
                    <span className="eyebrow">SECURE BENCHMARK SYNC</span>
                    <h1>안전한 결과 동기화</h1>
                    <p>외부 HTTPS 주소와 장치 fingerprint를 사용하는 새 v2 연결을 준비합니다.</p>
                </div>
            </header>

            <p className="benchmark-sync-warning">main의 동기화는 v2 전용입니다. 일회용 코드, Bearer 토큰, 로컬 IP 주소는 사용하지 않습니다.</p>
            {error && <p className="form-error" role="alert">{error}</p>}
            {notice && <p className="form-notice">{notice}</p>}

            <section className="benchmark-sync-grid" aria-label="v2 동기화 준비">
                <section className="benchmark-sync-card">
                    <div className="benchmark-sync-card-heading">
                        <div><span className="eyebrow">THIS DEVICE</span><h2>장치 신원</h2></div>
                    </div>
                    <form className="benchmark-sync-device-form" onSubmit={submitDeviceName}>
                        <label>
                            <span>장치 이름</span>
                            <input value={deviceName} onChange={(event) => setDeviceName(event.target.value)} maxLength={80} disabled={Boolean(busyAction)}/>
                        </label>
                        <button className="secondary-button" type="submit" disabled={Boolean(busyAction) || deviceName === state?.deviceName}>저장</button>
                    </form>
                    <p className="benchmark-sync-device-id">서명 fingerprint</p>
                    <button className="benchmark-sync-address" type="button" onClick={() => void copy(state?.identity.signingFingerprint || '', '서명 fingerprint')} disabled={!state?.identity.signingFingerprint} title="클릭하여 복사">
                        {state?.identity.signingFingerprint || '불러오는 중…'}
                    </button>
                    <p className="benchmark-sync-device-id">암호화 fingerprint</p>
                    <button className="benchmark-sync-address" type="button" onClick={() => void copy(state?.identity.keyAgreementFingerprint || '', '암호화 fingerprint')} disabled={!state?.identity.keyAgreementFingerprint} title="클릭하여 복사">
                        {state?.identity.keyAgreementFingerprint || '불러오는 중…'}
                    </button>
                    <small>개인키는 이 PC의 운영체제 비밀 저장소에만 보관됩니다.</small>
                </section>

                <section className="benchmark-sync-card">
                    <div className="benchmark-sync-card-heading">
                        <div><span className="eyebrow">DIRECT HTTPS RECEIVER</span><h2>직접 HTTPS 수신</h2></div>
                    </div>
                    <form className="benchmark-sync-connect-form" onSubmit={submitDirectTLS}>
                        <label>
                            <span>이 장치가 제공할 주소</span>
                            <input value={publicHTTPSURL} onChange={(event) => setPublicHTTPSURL(event.target.value)} placeholder="예: https://sync.example.com" disabled={Boolean(busyAction)}/>
                        </label>
                        <label>
                            <span>인증서 PEM 경로</span>
                            <input value={certificatePath} onChange={(event) => setCertificatePath(event.target.value)} placeholder="예: /Users/me/certs/fullchain.pem" disabled={Boolean(busyAction)}/>
                        </label>
                        <label>
                            <span>개인키 PEM 경로</span>
                            <input value={privateKeyPath} onChange={(event) => setPrivateKeyPath(event.target.value)} placeholder="예: /Users/me/certs/privkey.pem" disabled={Boolean(busyAction)}/>
                        </label>
                        <label>
                            <span>수신 주소</span>
                            <input value={listenAddress} onChange={(event) => setListenAddress(event.target.value)} placeholder="예: :8443" disabled={Boolean(busyAction)}/>
                        </label>
                        <button className="primary-button" type="submit" disabled={Boolean(busyAction) || !publicHTTPSURL.trim() || !certificatePath.trim() || !privateKeyPath.trim() || !listenAddress.trim()}>HTTPS 수신 시작</button>
                    </form>
                    {state?.endpointStatus === 'listening' ? <div className="benchmark-sync-endpoint-status"><p className="form-notice">v2 HTTPS 수신 endpoint가 실행 중입니다. 현재는 안전한 상태 확인만 제공하며, 다음 단계에서 페어링을 엽니다.</p><button className="text-button" type="button" disabled={Boolean(busyAction)} onClick={() => void runAction('stop-endpoint', () => ChatService.StopBenchmarkSyncV2Endpoint(), 'v2 HTTPS 수신 endpoint를 중지했습니다.')}>수신 중지</button></div> : state?.endpointStatus === 'configured' ? <p className="benchmark-sync-empty">직접 TLS 설정을 저장했습니다. 앱을 다시 열면 자동으로 수신을 다시 시작합니다.</p> : state?.endpointStatus === 'needs-tls-configuration' ? <p className="benchmark-sync-empty">외부 HTTPS 주소는 저장되어 있습니다. 인증서 PEM, 개인키 PEM, 수신 주소를 입력해 직접 TLS 수신을 시작해 주세요.</p> : <p className="benchmark-sync-empty">공개 CA 또는 조직 CA 인증서를 준비해 주세요. 앱은 인증서의 도메인·개인키 일치를 확인하고, 연결하는 장치는 TLS 인증서 신뢰를 별도로 검증합니다.</p>}
                </section>
            </section>

            <section className="benchmark-sync-grid" aria-label="v2 페어링">
                <section className="benchmark-sync-card">
                    <div className="benchmark-sync-card-heading">
                        <div><span className="eyebrow">INVITE</span><h2>다른 장치 초대</h2></div>
                        <button className="secondary-button" type="button" disabled={Boolean(busyAction) || state?.endpointStatus !== 'listening'} onClick={() => void createInvitation()}>초대 만들기</button>
                    </div>
                    {state?.endpointStatus !== 'listening' ? <p className="benchmark-sync-empty">먼저 직접 HTTPS 수신을 시작해 주세요.</p> : invitation ? <div className="benchmark-sync-invitation">
                        <p>아래 값은 <time>{formatTime(invitation.expiresAt)}</time>까지 한 번만 사용할 수 있습니다.</p>
                        <span>HTTPS 주소</span>
                        <button className="benchmark-sync-address" type="button" onClick={() => void copy(invitation.publicHTTPSURL, 'HTTPS 주소')}>{invitation.publicHTTPSURL}</button>
                        <span>초대 secret</span>
                        <button className="benchmark-sync-address" type="button" onClick={() => void copy(invitation.pairingSecret, '초대 secret')}>{invitation.pairingSecret}</button>
                        <small>이 값을 화면 캡처·로그·공개 채널에 남기지 마세요. 상대와는 별도 수단으로 fingerprint도 비교해야 합니다.</small>
                    </div> : <p className="benchmark-sync-empty">초대는 128비트 이상 secret과 현재 장치 fingerprint를 사용합니다.</p>}
                </section>

                <section className="benchmark-sync-card">
                    <div className="benchmark-sync-card-heading"><div><span className="eyebrow">JOIN</span><h2>초대에 연결</h2></div></div>
                    <form className="benchmark-sync-connect-form" onSubmit={startPairing}>
                        <label><span>상대 HTTPS 주소</span><input value={pairingURL} onChange={(event) => setPairingURL(event.target.value)} placeholder="예: https://sync.example.com" disabled={Boolean(busyAction)}/></label>
                        <label><span>초대 secret</span><input type="password" value={pairingSecret} onChange={(event) => setPairingSecret(event.target.value)} autoComplete="off" disabled={Boolean(busyAction)}/></label>
                        <button className="primary-button" type="submit" disabled={Boolean(busyAction) || !pairingURL.trim() || !pairingSecret.trim()}>연결 요청 보내기</button>
                    </form>
                    {(state?.outgoingPairings || []).map((pairing) => <div className="benchmark-sync-pairing" key={pairing.requestID}>
                        <div><strong>{pairing.host.deviceName}</strong><small>{pairing.status === 'approved' ? '상대 승인 완료' : '상대 승인 대기'}</small></div>
                        <button className="benchmark-sync-address" type="button" onClick={() => void copy(pairing.host.signingFingerprint, '상대 서명 fingerprint')}>{pairing.host.signingFingerprint}</button>
                        {pairing.status === 'approved' ? <button className="primary-button" type="button" disabled={Boolean(busyAction)} onClick={() => void runAction(`confirm-${pairing.requestID}`, () => ChatService.ConfirmBenchmarkSyncV2Pairing(pairing.requestID), 'fingerprint 확인을 마치고 장치를 신뢰했습니다.')}>fingerprint 일치, 연결 확정</button> : <button className="secondary-button" type="button" disabled={Boolean(busyAction)} onClick={() => void checkPairing(pairing.requestID)}>승인 상태 확인</button>}
                    </div>)}
                </section>
            </section>

            {(state?.pendingPairings || []).length > 0 && <section className="benchmark-sync-card">
                <div className="benchmark-sync-card-heading"><div><span className="eyebrow">ACTION REQUIRED</span><h2>받은 연결 요청</h2></div></div>
                {(state?.pendingPairings || []).map((pairing) => <div className="benchmark-sync-pairing" key={pairing.requestID}>
                    <div><strong>{pairing.requester.deviceName}</strong><small>{formatTime(pairing.expiresAt)}까지 승인할 수 있습니다.</small></div>
                    <button className="benchmark-sync-address" type="button" onClick={() => void copy(pairing.requester.signingFingerprint, '요청 장치 서명 fingerprint')}>{pairing.requester.signingFingerprint}</button>
                    <div className="benchmark-sync-pairing-actions"><button className="primary-button" type="button" disabled={Boolean(busyAction)} onClick={() => void runAction(`approve-${pairing.requestID}`, () => ChatService.ApproveBenchmarkSyncV2Pairing(pairing.requestID), 'fingerprint를 확인한 요청을 승인했습니다.')}>승인</button><button className="secondary-button" type="button" disabled={Boolean(busyAction)} onClick={() => void runAction(`reject-${pairing.requestID}`, () => ChatService.RejectBenchmarkSyncV2Pairing(pairing.requestID), '연결 요청을 거절했습니다.')}>거절</button></div>
                </div>)}
            </section>}

            <section className="benchmark-sync-card">
                <div className="benchmark-sync-card-heading"><div><span className="eyebrow">TRUSTED DEVICES</span><h2>연결한 장치</h2></div></div>
                {(state?.peers || []).length === 0 ? <p className="benchmark-sync-empty">아직 신뢰한 장치가 없습니다. 페어링에서는 양쪽 사용자가 fingerprint를 확인해야 합니다.</p> : <div className="benchmark-sync-log-list">{(state?.peers || []).map((peer) => <div className="benchmark-sync-log completed" key={peer.signingKeyID}><div><strong>{peer.deviceName}</strong><small>{peer.publicHTTPSURL || '외부 수신 주소 없음'} · {peer.signingFingerprint}</small></div><time>{formatTime(peer.trustedAt)}</time></div>)}</div>}
            </section>

            <section className="benchmark-sync-card benchmark-sync-logs">
                <div className="benchmark-sync-card-heading">
                    <div><span className="eyebrow">LOCAL AUDIT</span><h2>보안 활동 기록</h2></div>
                    <button className="text-button" type="button" disabled={Boolean(busyAction) || logs.length === 0} onClick={() => void runAction('clear-logs', () => ChatService.ClearBenchmarkSyncV2Logs(), '보안 활동 기록을 지웠습니다.')}>기록 지우기</button>
                </div>
                {logs.length === 0 ? <p className="benchmark-sync-empty">아직 기록이 없습니다. 키·연결·검증 과정의 요약만 이 PC에 남기며, 비밀값과 벤치마크 본문은 기록하지 않습니다.</p> : <div className="benchmark-sync-log-list">
                    {logs.map((log) => <div className={`benchmark-sync-log ${log.status}`} key={log.id}>
                        <div><strong>{log.event}</strong>{log.message && <small>{log.message}</small>}</div>
                        <time>{formatTime(log.occurredAt)}</time>
                    </div>)}
                </div>}
            </section>
        </main>
    );
}
