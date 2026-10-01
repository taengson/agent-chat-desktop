import {FormEvent, useEffect, useMemo, useState} from 'react';

export interface OpenRouterModelOption {
    id: string;
    ownedBy?: string;
}

interface OpenRouterModelPickerProps {
    open: boolean;
    models: OpenRouterModelOption[];
    selectedModel: string;
    selectedModelIDs: string[];
    onClose: () => void;
    onApply: (modelIDs: string[]) => void;
}

function providerName(modelID: string): string {
    const [provider] = modelID.split('/');
    return provider || '기타';
}

function isFreeModel(modelID: string): boolean {
    return modelID.toLocaleLowerCase().endsWith(':free');
}

export function isOpenRouterURL(baseURL: string): boolean {
    return baseURL.toLowerCase().includes('openrouter.ai');
}

export default function OpenRouterModelPicker({
    open,
    models,
    selectedModel,
    selectedModelIDs,
    onClose,
    onApply,
}: OpenRouterModelPickerProps) {
    const [query, setQuery] = useState('');
    const [provider, setProvider] = useState('');
    const [draftModelIDs, setDraftModelIDs] = useState<string[]>([]);

    const providers = useMemo(() => {
        const counts = new Map<string, number>();
        for (const model of models) {
            const name = providerName(model.id);
            counts.set(name, (counts.get(name) || 0) + 1);
        }
        return Array.from(counts, ([name, count]) => ({name, count}))
            .sort((left, right) => right.count - left.count || left.name.localeCompare(right.name));
    }, [models]);
    const freeModelCount = useMemo(() => models.filter((model) => isFreeModel(model.id)).length, [models]);

    const visibleModels = useMemo(() => {
        const normalizedQuery = query.trim().toLocaleLowerCase();
        return models
            .filter((model) => !provider || (provider === 'free' ? isFreeModel(model.id) : providerName(model.id) === provider))
            .filter((model) => !normalizedQuery || model.id.toLocaleLowerCase().includes(normalizedQuery))
            .sort((left, right) => left.id.localeCompare(right.id))
            .slice(0, 80);
    }, [models, provider, query]);

    const hasExactMatch = useMemo(() => {
        const normalizedQuery = query.trim().toLocaleLowerCase();
        return Boolean(normalizedQuery) && models.some((model) => model.id.toLocaleLowerCase() === normalizedQuery);
    }, [models, query]);

    useEffect(() => {
        if (!open) return;
        setQuery('');
        setProvider('');
        setDraftModelIDs(selectedModelIDs);
    }, [open, selectedModelIDs]);

    useEffect(() => {
        if (!open) return;
        const closeOnEscape = (event: KeyboardEvent) => {
            if (event.key === 'Escape') onClose();
        };
        window.addEventListener('keydown', closeOnEscape);
        return () => window.removeEventListener('keydown', closeOnEscape);
    }, [onClose, open]);

    if (!open) return null;

    function toggleModel(modelID: string) {
        setDraftModelIDs((current) => current.includes(modelID)
            ? current.filter((item) => item !== modelID)
            : [...current, modelID]);
    }

    function addTypedModel(event: FormEvent<HTMLFormElement>) {
        event.preventDefault();
        const modelID = query.trim();
        if (!modelID) return;
        setDraftModelIDs((current) => current.includes(modelID) ? current : [...current, modelID]);
        setQuery('');
        setProvider('');
    }

    function applySelection() {
        if (draftModelIDs.length === 0) return;
        onApply(draftModelIDs);
        onClose();
    }

    return (
        <div className="model-picker-backdrop" role="presentation" onMouseDown={onClose}>
            <section
                className="model-picker-dialog"
                role="dialog"
                aria-modal="true"
                aria-labelledby="openrouter-model-picker-title"
                onMouseDown={(event) => event.stopPropagation()}
            >
                <header className="model-picker-header">
                    <div>
                        <span>OPENROUTER</span>
                        <h2 id="openrouter-model-picker-title">모델 선택</h2>
                        <p>{models.length}개 모델 중 필요한 모델만 골라 채팅과 실험 목록에 넣습니다.</p>
                    </div>
                    <button className="model-picker-close" type="button" onClick={onClose} aria-label="모델 선택 창 닫기">×</button>
                </header>

                <form className="model-picker-search" onSubmit={addTypedModel}>
                    <input
                        value={query}
                        onChange={(event) => setQuery(event.target.value)}
                        placeholder="예: qwen, claude, deepseek 또는 전체 모델 ID"
                        type="search"
                        spellCheck={false}
                        autoFocus
                    />
                    <button type="submit" disabled={!query.trim()}>ID 추가</button>
                </form>

                <div className="model-picker-filters" aria-label="공급자 필터">
                    <button className={!provider ? 'active' : ''} type="button" onClick={() => setProvider('')}>전체</button>
                    <button className={provider === 'free' ? 'active' : ''} type="button" onClick={() => setProvider((current) => current === 'free' ? '' : 'free')}>
                        free <small>{freeModelCount}</small>
                    </button>
                    {providers.map((item) => (
                        <button
                            className={provider === item.name ? 'active' : ''}
                            key={item.name}
                            type="button"
                            onClick={() => setProvider((current) => current === item.name ? '' : item.name)}
                        >
                            {item.name} <small>{item.count}</small>
                        </button>
                    ))}
                </div>

                <div className="model-picker-results" aria-label="검색된 모델">
                    {visibleModels.map((model) => (
                        <button
                            className={draftModelIDs.includes(model.id) ? 'selected' : ''}
                            key={model.id}
                            type="button"
                            onClick={() => toggleModel(model.id)}
                        >
                            <strong>{model.id}</strong>
                            {model.ownedBy && <small>{model.ownedBy}</small>}
                            {model.id === selectedModel
                                ? <span>현재 사용</span>
                                : draftModelIDs.includes(model.id) && <span>선택됨</span>}
                        </button>
                    ))}
                    {visibleModels.length === 0 && (
                        <p>일치하는 모델이 없습니다. 정확한 모델 ID를 알고 있다면 위 입력칸에서 그대로 선택할 수 있습니다.</p>
                    )}
                </div>
                {visibleModels.length === 80 && (
                    <p className="model-picker-limit">결과가 많습니다. 검색어나 공급자를 더 추가해 좁혀 주세요.</p>
                )}
                {query.trim() && !hasExactMatch && visibleModels.length > 0 && <p className="model-picker-direct-note">정확한 모델 ID를 알고 있다면 위의 ‘ID 추가’로 목록에 넣을 수 있습니다.</p>}
                <footer className="model-picker-actions">
                    <span>{draftModelIDs.length}개 모델 선택됨</span>
                    <div>
                        <button className="dialog-cancel-button" type="button" onClick={onClose}>취소</button>
                        <button className="model-picker-apply" type="button" onClick={applySelection} disabled={draftModelIDs.length === 0}>선택 완료</button>
                    </div>
                </footer>
            </section>
        </div>
    );
}
