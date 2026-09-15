import {ChangeEvent, FormEvent, KeyboardEvent, useCallback, useEffect, useMemo, useRef, useState, WheelEvent} from 'react';
import {Dialogs, Events} from '@wailsio/runtime';
import type {TextContent} from 'pdfjs-dist/types/src/display/api';
import ReactMarkdown from 'react-markdown';
import {renderToStaticMarkup} from 'react-dom/server';
import remarkGfm from 'remark-gfm';
import {App as ChatService} from '../bindings/github.com/taengson/agent-chat-desktop';
import type {
    ChatEvent,
    ChatAttachment,
    ChatRequest,
    Conversation,
    ConversationMessage,
    ConversationSummary,
    ModelBenchmarkSummary,
    ResponseMetrics,
    SavedConnectionProfile,
    TokenUsage,
} from '../bindings/github.com/taengson/agent-chat-desktop/models';
import ModelBenchmarkWorkspace, {type ModelBenchmarkSidebarState} from './ModelBenchmark';
import BenchmarkSyncWorkspace from './BenchmarkSync';
import OpenRouterModelPicker, {isOpenRouterURL} from './OpenRouterModelPicker';
import {messagesForModel} from './chatContext';
import {
    createAttachmentChunks,
    selectAttachmentContent,
    textByteSize,
    type AttachmentChunk,
} from './attachmentContent';
import {
    reasoningEffortOptions,
    reasoningEffortWarning,
    type ReasoningEffort,
} from './reasoningEffort';
import './App.css';

type Role = 'user' | 'assistant';
type MessageStatus = 'complete' | 'streaming' | 'cancelled' | 'failed';
type ChatShareContentMode = 'question-answer' | 'answer';
type ChatShareFormat = 'html' | 'markdown';

interface UIMessage {
    id: string;
    role: Role;
    content: string;
    status: MessageStatus;
    model?: string;
    attachments: ChatAttachment[];
    usage?: TokenUsage;
    metrics?: ResponseMetrics;
}

interface AttachmentDraft {
    name: string;
    size: number;
    source: string;
    chunks: AttachmentChunk[];
}

interface ChatShareTarget {
    answer: UIMessage;
    question: UIMessage | null;
}

interface ModelOption {
    id: string;
    ownedBy?: string;
}

type ConnectionProfileOption = SavedConnectionProfile & {
    isBuiltIn?: boolean;
};

interface ModelTokenUsage {
    promptTokens: number;
    completionTokens: number;
    totalTokens: number;
}

type BenchmarkHistorySection = 'recent' | 'imported' | 'synchronized';

interface BenchmarkHistorySectionOpenState {
    recent: boolean;
    imported: boolean;
    synchronized: boolean;
}

const defaultBaseURL = 'http://localhost:8000';
const openRouterProfile: ConnectionProfileOption = {
    id: 'builtin-openrouter',
    name: 'OpenRouter',
    baseURL: 'https://openrouter.ai/api/v1',
    isBuiltIn: true,
};
const structuredAttachmentFileExtensions = new Set(['docx', 'xlsx']);
const attachmentFileExtensions = new Set([
    'txt', 'md', 'csv', 'json', 'jsonl', 'xml', 'yaml', 'yml', 'toml', 'ini', 'log',
    'js', 'jsx', 'ts', 'tsx', 'mjs', 'cjs', 'go', 'py', 'java', 'kt', 'kts', 'rb',
    'php', 'c', 'h', 'cc', 'cpp', 'cxx', 'hpp', 'cs', 'rs', 'swift', 'sh', 'zsh',
    'sql', 'html', 'css', 'scss', 'less', 'vue', 'svelte', 'graphql', 'gql',
    'pdf', ...structuredAttachmentFileExtensions,
]);
const attachmentAccept = Array.from(attachmentFileExtensions, (extension) => `.${extension}`).join(',');
const maxAttachmentsPerMessage = 4;
const maxAttachmentFileSize = 5 * 1024 * 1024;
const maxAttachmentTotalFileSize = 12 * 1024 * 1024;
const maxAttachmentContentSize = 240 * 1024;
const maxAttachmentTotalContentSize = 512 * 1024;
const benchmarkHistorySectionStateStorageKey = 'agent-chat.benchmark-history-section-open';
const emptyBenchmarkSidebar: ModelBenchmarkSidebarState = {
    model: '',
    profileName: '',
    profileBaseURL: '',
    status: 'idle',
    completedCaseCount: 0,
    caseCount: 0,
    recent: [],
    imported: [],
    synchronized: [],
    isHistoryLoading: false,
};

function loadBenchmarkHistorySectionOpenState(): BenchmarkHistorySectionOpenState {
    const fallback = {recent: true, imported: true, synchronized: true};
    if (typeof window === 'undefined') return fallback;
    try {
        const stored = window.localStorage.getItem(benchmarkHistorySectionStateStorageKey);
        if (!stored) return fallback;
        const parsed = JSON.parse(stored) as Partial<BenchmarkHistorySectionOpenState>;
        return {
            recent: typeof parsed.recent === 'boolean' ? parsed.recent : fallback.recent,
            imported: typeof parsed.imported === 'boolean' ? parsed.imported : fallback.imported,
            synchronized: typeof parsed.synchronized === 'boolean' ? parsed.synchronized : fallback.synchronized,
        };
    } catch {
        return fallback;
    }
}

function makeID(): string {
    if (typeof crypto !== 'undefined' && crypto.randomUUID) {
        return crypto.randomUUID();
    }
    return `${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

function appendAssistantDelta(content: string, delta: string): string {
    if (content !== '') {
        return content + delta;
    }

    return delta.replace(/^(?:[ \t]*\r?\n)+[ \t]*/, '');
}

function toUIMessage(message: ConversationMessage): UIMessage {
    return {
        id: message.id,
        role: message.role === 'user' ? 'user' : 'assistant',
        content: message.content,
        status: ['complete', 'streaming', 'cancelled', 'failed'].includes(message.status)
            ? message.status as MessageStatus
            : 'complete',
        model: message.model || undefined,
        attachments: message.attachments || [],
        usage: message.usage ?? undefined,
        metrics: message.metrics ?? undefined,
    };
}

function toStoredMessages(messages: UIMessage[]): ConversationMessage[] {
    return messages.map(({id, role, content, status, model, attachments, usage, metrics}) => ({
        id,
        role,
        content,
        status,
        model,
        attachments,
        usage,
        metrics,
    }));
}

function attachmentFileName(fileName: string): string {
    return fileName.trim() || '이름 없는 파일';
}

function attachmentExtension(fileName: string): string {
    const extension = fileName.split('.').pop();
    return extension && extension !== fileName ? extension.toLocaleLowerCase('en-US') : '';
}

type StructuredAttachmentFileType = 'docx' | 'xlsx';

function isStructuredAttachmentFileType(extension: string): extension is StructuredAttachmentFileType {
    return structuredAttachmentFileExtensions.has(extension);
}

function formatFileSize(bytes: number): string {
    if (bytes < 1_024) return `${bytes}B`;
    if (bytes < 1_024 * 1_024) {
        return `${new Intl.NumberFormat('ko-KR', {maximumFractionDigits: 0}).format(bytes / 1_024)}KB`;
    }
    return `${new Intl.NumberFormat('ko-KR', {maximumFractionDigits: 1}).format(bytes / (1_024 * 1_024))}MB`;
}

function escapeHTML(value: string): string {
    return value.replace(/[&<>"']/g, (character) => ({
        '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
    })[character] || character);
}

function shareAttachmentLines(message: UIMessage | null): string[] {
    if (!message?.attachments.length) return [];
    return message.attachments.map((attachment) => `- ${attachment.name} (${formatFileSize(attachment.size)})${attachmentDeliveryDescription(attachment)}`);
}

function attachmentDeliveryDescription(attachment: ChatAttachment): string {
    if (attachment.selectionSummary) return ` · ${attachment.selectionSummary}`;
    return attachment.truncated ? ' · 일부 발췌본이 모델에 전달됨' : '';
}

function formatShareTimestamp(): string {
    return new Intl.DateTimeFormat('ko-KR', {
        year: 'numeric', month: 'long', day: 'numeric', hour: '2-digit', minute: '2-digit',
    }).format(new Date());
}

function chatShareFilename(mode: ChatShareContentMode, format: ChatShareFormat): string {
    const timestamp = new Date().toISOString().replace(/[-:]/g, '').replace(/\.\d+Z$/, '').replace('T', '-');
    return `agent-chat-${mode === 'question-answer' ? 'question-answer' : 'answer'}-${timestamp}.${format === 'html' ? 'html' : 'md'}`;
}

function chatShareMarkdown(target: ChatShareTarget, mode: ChatShareContentMode): string {
    const lines = [
        '# Agent Chat 공유',
        '',
        `- 저장 시각: ${formatShareTimestamp()}`,
    ];
    if (mode === 'question-answer' && target.question) {
        lines.push('', '## 질문', '', target.question.content || '질문 내용이 없습니다.');
        const attachmentLines = shareAttachmentLines(target.question);
        if (attachmentLines.length > 0) {
            lines.push('', '### 첨부 파일', '', ...attachmentLines, '', '> 첨부 문서 본문은 공유 파일에 포함하지 않았습니다.');
        }
    }
    lines.push('', '## 응답', '', target.answer.content || '응답 내용이 없습니다.');
    return `${lines.join('\n')}\n`;
}

function renderMarkdownForShare(content: string): string {
    return renderToStaticMarkup(
        <ReactMarkdown remarkPlugins={[remarkGfm]}>{content || '응답 내용이 없습니다.'}</ReactMarkdown>,
    );
}

function chatShareHTML(target: ChatShareTarget, mode: ChatShareContentMode): string {
    const questionHTML = mode === 'question-answer' && target.question ? (() => {
        const attachments = shareAttachmentLines(target.question);
        const attachmentHTML = attachments.length > 0
            ? `<section><h3>첨부 파일</h3><ul>${attachments.map((item) => `<li>${escapeHTML(item)}</li>`).join('')}</ul><p class="notice">첨부 문서 본문은 공유 파일에 포함하지 않았습니다.</p></section>`
            : '';
        return `<section><h2>질문</h2><pre>${escapeHTML(target.question.content || '질문 내용이 없습니다.')}</pre>${attachmentHTML}</section>`;
    })() : '';
    return `<!doctype html>
<html lang="ko">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Agent Chat 공유</title>
  <style>
    :root { color: #292925; background: #f6f6f3; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
    body { max-width: 860px; margin: 0 auto; padding: 42px 24px 64px; line-height: 1.6; }
    main { padding: 28px; border: 1px solid #deded7; border-radius: 14px; background: #fff; box-shadow: 0 6px 22px rgba(31,31,27,.05); }
    h1, h2, h3, p { margin-top: 0; } h1 { margin-bottom: 4px; font-size: 26px; } h2 { margin-top: 28px; font-size: 18px; } h3 { margin: 18px 0 8px; color: #56778a; font-size: 13px; }
    .date, .notice { color: #77776f; font-size: 13px; } section > pre { margin: 0; padding: 15px; overflow-wrap: anywhere; border: 1px solid #e1e1db; border-radius: 8px; background: #fbfbf9; white-space: pre-wrap; font: 13px/1.7 ui-monospace, SFMono-Regular, Menlo, monospace; } ul { margin: 0; padding-left: 20px; font-size: 13px; }
    .markdown-body > :first-child { margin-top: 0; } .markdown-body > :last-child { margin-bottom: 0; } .markdown-body h1, .markdown-body h2, .markdown-body h3, .markdown-body h4 { margin: 1.5em 0 .55em; line-height: 1.35; } .markdown-body h1 { font-size: 24px; } .markdown-body h2 { font-size: 20px; } .markdown-body h3 { font-size: 16px; color: #292925; } .markdown-body p { margin: .75em 0; } .markdown-body ul, .markdown-body ol { margin: .75em 0; padding-left: 25px; font-size: inherit; } .markdown-body li + li { margin-top: .25em; } .markdown-body a { color: #26627d; } .markdown-body blockquote { margin: 1em 0; padding: .15em 1em; border-left: 3px solid #9ab9c8; color: #55554f; background: #f7fafb; } .markdown-body code { padding: .12em .34em; border-radius: 4px; background: #f0f0ec; font: .9em ui-monospace, SFMono-Regular, Menlo, monospace; } .markdown-body pre { margin: 1em 0; padding: 15px; overflow-x: auto; border: 1px solid #e1e1db; border-radius: 8px; background: #fbfbf9; font: 13px/1.7 ui-monospace, SFMono-Regular, Menlo, monospace; } .markdown-body pre code { padding: 0; background: transparent; font: inherit; } .markdown-body table { display: block; max-width: 100%; margin: 1em 0; overflow-x: auto; border-collapse: collapse; } .markdown-body th, .markdown-body td { padding: 8px 10px; border: 1px solid #deded7; text-align: left; } .markdown-body th { background: #f5f5f1; } .markdown-body hr { margin: 1.5em 0; border: 0; border-top: 1px solid #deded7; } .markdown-body img { max-width: 100%; height: auto; }
    @media print { body { padding: 20px; background: #fff; } main { border: 0; box-shadow: none; } }
  </style>
</head>
<body><main>
  <header><h1>Agent Chat 공유</h1><p class="date">저장 시각: ${escapeHTML(formatShareTimestamp())}</p></header>
  ${questionHTML}
  <section><h2>응답</h2><div class="markdown-body">${renderMarkdownForShare(target.answer.content)}</div></section>
</main></body>
</html>`;
}

function pdfPageText(items: TextContent['items']): string {
    return items.map((item) => {
        if (!item || typeof item !== 'object' || !('str' in item) || typeof item.str !== 'string') {
            return '';
        }
        return `${item.str}${'hasEOL' in item && item.hasEOL ? '\n' : ' '}`;
    }).join('').replace(/[ \t]+\n/g, '\n').trim();
}

async function streamPDFPageText(page: {streamTextContent: () => ReadableStream<Pick<TextContent, 'items'>>}): Promise<string> {
    const reader = page.streamTextContent().getReader();
    const items: TextContent['items'] = [];
    try {
        while (true) {
            const {done, value} = await reader.read();
            if (done) break;
            items.push(...value.items);
        }
    } finally {
        reader.releaseLock();
    }
    return pdfPageText(items);
}

async function readPDFContent(file: File): Promise<string> {
    const [{getDocument, GlobalWorkerOptions}, {default: pdfWorkerURL}] = await Promise.all([
        import('pdfjs-dist'),
        import('pdfjs-dist/build/pdf.worker.mjs?url'),
    ]);
    GlobalWorkerOptions.workerSrc = pdfWorkerURL;
    const loadingTask = getDocument({data: new Uint8Array(await file.arrayBuffer())});

    try {
        const document = await loadingTask.promise;
        const pageNumbers = document.numPages <= 100
            ? Array.from({length: document.numPages}, (_, index) => index + 1)
            : [
                ...Array.from({length: 70}, (_, index) => index + 1),
                ...Array.from({length: 30}, (_, index) => document.numPages - 29 + index),
            ];
        const pages: string[] = [];
        let previousPage = 0;
        for (const pageNumber of pageNumbers) {
            if (previousPage > 0 && pageNumber > previousPage + 1) {
                pages.push(`[PDF ${previousPage + 1}~${pageNumber - 1}쪽은 길이 제한으로 생략됨]`);
            }
            const page = await document.getPage(pageNumber);
            const text = await streamPDFPageText(page);
            if (text) pages.push(`[PDF ${pageNumber}쪽]\n${text}`);
            previousPage = pageNumber;
        }

        const text = pages.join('\n\n').trim();
        if (!text) {
            throw new Error(`“${attachmentFileName(file.name)}”에서 읽을 수 있는 텍스트를 찾지 못했습니다. 스캔된 PDF는 아직 지원하지 않습니다.`);
        }
        return text;
    } finally {
        await loadingTask.destroy();
    }
}

function documentTextFromHTML(html: string): string {
    const document = new DOMParser().parseFromString(html, 'text/html');
    const lines: string[] = [];
    for (const element of document.body.querySelectorAll('h1, h2, h3, h4, h5, h6, p, li, tr')) {
        if (element.tagName === 'P' && element.closest('tr')) continue;
        const text = element.tagName === 'TR'
            ? Array.from(element.querySelectorAll('th, td'))
                .map((cell) => cell.textContent?.replace(/\s+/g, ' ').trim() || '')
                .filter(Boolean)
                .join('\t')
            : element.textContent?.replace(/\s+/g, ' ').trim() || '';
        if (!text) continue;
        if (/^H[1-6]$/.test(element.tagName)) {
            lines.push(`# ${text}`);
        } else {
            lines.push(text);
        }
    }
    return lines.join('\n\n').trim();
}

async function readDOCXAttachmentContent(file: File): Promise<Pick<AttachmentDraft, 'source' | 'chunks'>> {
    const mammoth = await import('mammoth');
    const result = await mammoth.convertToHtml(
        {arrayBuffer: await file.arrayBuffer()},
        {
            externalFileAccess: false,
            includeEmbeddedStyleMap: false,
            convertImage: mammoth.images.imgElement(async () => ({src: ''})),
        },
    );
    const source = documentTextFromHTML(result.value);
    if (!source) {
        throw new Error(`“${attachmentFileName(file.name)}”에서 읽을 수 있는 텍스트를 찾지 못했습니다.`);
    }
    return {source, chunks: createAttachmentChunks(source)};
}

function spreadsheetCellText(value: unknown): string {
    if (value === null || value === undefined) return '';
    if (value instanceof Date) return value.toISOString().slice(0, 10);
    return String(value).replace(/[\t\r\n]+/g, ' ').trim();
}

async function readXLSXAttachmentContent(file: File): Promise<Pick<AttachmentDraft, 'source' | 'chunks'>> {
    const {default: readXLSXFile} = await import('read-excel-file/browser');
    const sheets = await readXLSXFile(file);
    const lines: string[] = [];
    let cellCount = 0;
    const maxSpreadsheetCells = 250_000;
    for (const sheet of sheets) {
        lines.push(`# ${sheet.sheet} 시트`);
        for (const row of sheet.data) {
            cellCount += row.length;
            if (cellCount > maxSpreadsheetCells) {
                lines.push(`[셀 ${maxSpreadsheetCells.toLocaleString('ko-KR')}개 이후 내용은 읽지 않았습니다.]`);
                break;
            }
            const values = row.map(spreadsheetCellText);
            if (values.some(Boolean)) lines.push(values.join('\t'));
        }
        if (cellCount > maxSpreadsheetCells) break;
        lines.push('');
    }
    const source = lines.join('\n').trim();
    if (!source || sheets.every((sheet) => sheet.data.every((row) => row.every((value) => spreadsheetCellText(value) === '')))) {
        throw new Error(`“${attachmentFileName(file.name)}”에서 읽을 수 있는 텍스트를 찾지 못했습니다.`);
    }
    return {source, chunks: createAttachmentChunks(source)};
}

async function readAttachmentContent(file: File, name: string): Promise<Pick<AttachmentDraft, 'source' | 'chunks'>> {
    const extension = attachmentExtension(name);
    if (extension === 'pdf') {
        const source = await readPDFContent(file);
        return {source, chunks: createAttachmentChunks(source)};
    }
    if (isStructuredAttachmentFileType(extension)) {
        return extension === 'docx'
            ? readDOCXAttachmentContent(file)
            : readXLSXAttachmentContent(file);
    }
    const content = await file.text();
    if (content.includes('\0')) {
        throw new Error(`“${name}”은 텍스트 파일로 읽을 수 없습니다.`);
    }
    return {source: content, chunks: createAttachmentChunks(content)};
}

function summaryFromConversation(conversation: Conversation): ConversationSummary {
    return {
        id: conversation.id,
        title: conversation.title,
        updatedAt: conversation.updatedAt,
        messageCount: conversation.messages?.length || 0,
    };
}

function sortConversations(conversations: ConversationSummary[]): ConversationSummary[] {
    return [...conversations].sort((left, right) => right.updatedAt.localeCompare(left.updatedAt));
}

function sortSavedConnectionProfiles(profiles: ConnectionProfileOption[]): ConnectionProfileOption[] {
    return [...profiles].sort((left, right) => {
        if (left.isBuiltIn !== right.isBuiltIn) return left.isBuiltIn ? -1 : 1;
        return left.name.localeCompare(right.name, 'ko-KR');
    });
}

function withBuiltInConnectionProfiles(profiles: SavedConnectionProfile[]): ConnectionProfileOption[] {
    return sortSavedConnectionProfiles([
        openRouterProfile,
        ...profiles.filter((profile) => profile.id !== openRouterProfile.id),
    ]);
}

function formatUpdatedAt(value: string): string {
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) {
        return '';
    }
    return new Intl.DateTimeFormat('ko-KR', {month: 'short', day: 'numeric'}).format(date);
}

function canSaveConnectionProfile(baseURL: string): boolean {
    try {
        const parsed = new URL(baseURL);
        return (parsed.protocol === 'http:' || parsed.protocol === 'https:')
            && parsed.host !== ''
            && parsed.username === ''
            && parsed.password === ''
            && parsed.search === ''
            && parsed.hash === '';
    } catch {
        return false;
    }
}

function formatTokenCount(value: number): string {
    if (value < 1_000) {
        return new Intl.NumberFormat('ko-KR').format(value);
    }
    const shortened = value / 1_000;
    return `${new Intl.NumberFormat('ko-KR', {
        maximumFractionDigits: shortened < 100 ? 1 : 0,
    }).format(shortened)}K`;
}

function formatDuration(milliseconds: number): string {
    const seconds = milliseconds / 1_000;
    return `${new Intl.NumberFormat('ko-KR', {
        minimumFractionDigits: seconds < 10 ? 1 : 0,
        maximumFractionDigits: seconds < 10 ? 1 : 0,
    }).format(seconds)}초`;
}

function formatGenerationSpeed(usage?: TokenUsage, metrics?: ResponseMetrics): string | null {
    if (!usage || !metrics || usage.completionTokens <= 0 || metrics.firstTokenDurationMs <= 0) {
        return null;
    }
    const generationDuration = metrics.totalDurationMs - metrics.firstTokenDurationMs;
    if (generationDuration <= 0) {
        return null;
    }
    const tokensPerSecond = usage.completionTokens / (generationDuration / 1_000);
    return `생성 속도 ${new Intl.NumberFormat('ko-KR', {maximumFractionDigits: 1}).format(tokensPerSecond)} tok/s`;
}

type StructuredCodeLanguage = 'diff' | 'json';

interface StructuredCodeBlock {
    language: StructuredCodeLanguage;
    content: string;
}

function isUnifiedDiff(content: string): boolean {
    const lines = content.split(/\r?\n/);
    const hasOldFile = lines.some((line) => line.startsWith('--- '));
    const hasNewFile = lines.some((line) => line.startsWith('+++ '));
    const hasChange = lines.some((line) => line.startsWith('@@ ')
        || line.startsWith('+') && !line.startsWith('+++ ')
        || line.startsWith('-') && !line.startsWith('--- '));

    return hasOldFile && hasNewFile && hasChange;
}

function detectStructuredCodeBlock(content: string): StructuredCodeBlock | null {
    const trimmed = content.trim();
    if (trimmed === '') {
        return null;
    }
    if (isUnifiedDiff(trimmed)) {
        return {language: 'diff', content: trimmed};
    }

    try {
        const parsed = JSON.parse(trimmed);
        if (typeof parsed === 'object' && parsed !== null) {
            return {language: 'json', content: JSON.stringify(parsed, null, 2)};
        }
    } catch {
        // A normal Markdown message need not be valid JSON.
    }

    return null;
}

function diffLineClassName(line: string): string {
    if (line.startsWith('+++ ') || line.startsWith('--- ') || line.startsWith('@@ ') || line.startsWith('diff --git ') || line.startsWith('index ')) {
        return 'diff-meta';
    }
    if (line.startsWith('+')) {
        return 'diff-addition';
    }
    if (line.startsWith('-')) {
        return 'diff-removal';
    }
    return '';
}

function StructuredCode({block}: {block: StructuredCodeBlock}) {
    if (block.language === 'diff') {
        return (
            <pre className="detected-code-block"><code className="language-diff">{block.content.split(/\r?\n/).map((line, index) => (
                    <span className={`diff-line ${diffLineClassName(line)}`} key={`${index}-${line}`}>
                        {line || '\u00a0'}
                    </span>
                ))}</code></pre>
        );
    }

    return <pre className="detected-code-block"><code className="language-json">{block.content}</code></pre>;
}

function AssistantMessageContent({content}: {content: string}) {
    const structuredCodeBlock = detectStructuredCodeBlock(content);
    if (structuredCodeBlock) {
        return <StructuredCode block={structuredCodeBlock}/>;
    }

    return <ReactMarkdown remarkPlugins={[remarkGfm]}>{content}</ReactMarkdown>;
}

function App() {
    const [workspace, setWorkspace] = useState<'chat' | 'benchmark' | 'sync'>('chat');
    const [sidebarVisible, setSidebarVisible] = useState(true);
    const [benchmarkBusy, setBenchmarkBusy] = useState(false);
    const [benchmarkSidebar, setBenchmarkSidebar] = useState<ModelBenchmarkSidebarState>(emptyBenchmarkSidebar);
    const [benchmarkHistorySectionOpen, setBenchmarkHistorySectionOpen] = useState<BenchmarkHistorySectionOpenState>(loadBenchmarkHistorySectionOpenState);
    const [benchmarkOpenRequestID, setBenchmarkOpenRequestID] = useState<string | null>(null);
    const [benchmarkHistoryRefreshKey, setBenchmarkHistoryRefreshKey] = useState(0);
    const [benchmarkToDelete, setBenchmarkToDelete] = useState<ModelBenchmarkSummary | null>(null);
    const [deletingBenchmark, setDeletingBenchmark] = useState(false);
    const [benchmarkDeleteError, setBenchmarkDeleteError] = useState('');
    const [baseURL, setBaseURL] = useState(defaultBaseURL);
    const [apiKey, setAPIKey] = useState('');
    const [models, setModels] = useState<ModelOption[]>([]);
    const [selectedModel, setSelectedModel] = useState('');
    const [openRouterModelPickerOpen, setOpenRouterModelPickerOpen] = useState(false);
    const [openRouterModelIDs, setOpenRouterModelIDs] = useState<string[]>([]);
    const [modelTokenUsage, setModelTokenUsage] = useState<Record<string, ModelTokenUsage>>({});
    const [includeConversationHistory, setIncludeConversationHistory] = useState(false);
    const [loadingModels, setLoadingModels] = useState(false);
    const [connectionMessage, setConnectionMessage] = useState('서버 연결 전');
    const [connectionProfileReady, setConnectionProfileReady] = useState(false);
    const [savedConnectionProfiles, setSavedConnectionProfiles] = useState<ConnectionProfileOption[]>([openRouterProfile]);
    const [selectedSavedConnectionProfileID, setSelectedSavedConnectionProfileID] = useState('');
    const [connectionProfileName, setConnectionProfileName] = useState('');
    const [savingConnectionProfile, setSavingConnectionProfile] = useState(false);
    const [connectionProfileToDelete, setConnectionProfileToDelete] = useState<SavedConnectionProfile | null>(null);
    const [deletingConnectionProfile, setDeletingConnectionProfile] = useState(false);
    const [conversations, setConversations] = useState<ConversationSummary[]>([]);
    const [conversationSearch, setConversationSearch] = useState('');
    const [conversationsExpanded, setConversationsExpanded] = useState(true);
    const [connectionSettingsOpen, setConnectionSettingsOpen] = useState(false);
    const [activeConversation, setActiveConversation] = useState<Conversation | null>(null);
    const [loadingConversations, setLoadingConversations] = useState(true);
    const [conversationToDelete, setConversationToDelete] = useState<Conversation | null>(null);
    const [deletingConversation, setDeletingConversation] = useState(false);
    const [renamingConversation, setRenamingConversation] = useState(false);
    const [conversationTitleDraft, setConversationTitleDraft] = useState('');
    const [messages, setMessages] = useState<UIMessage[]>([]);
    const [input, setInput] = useState('');
    const [reasoningEffort, setReasoningEffort] = useState<ReasoningEffort>('');
    const [attachments, setAttachments] = useState<AttachmentDraft[]>([]);
    const [busy, setBusy] = useState(false);
    const [cancelling, setCancelling] = useState(false);
    const [copiedMessageID, setCopiedMessageID] = useState<string | null>(null);
    const [shareTarget, setShareTarget] = useState<ChatShareTarget | null>(null);
    const [shareContentMode, setShareContentMode] = useState<ChatShareContentMode>('question-answer');
    const [sharingFormat, setSharingFormat] = useState<ChatShareFormat | null>(null);
    const [shareError, setShareError] = useState('');
    const [error, setError] = useState('');
    const [canScrollUp, setCanScrollUp] = useState(false);
    const [showScrollToLatest, setShowScrollToLatest] = useState(false);

    const activeRequestRef = useRef<string | null>(null);
    const assistantMessageRef = useRef<string | null>(null);
    const activeRequestModelRef = useRef<string | null>(null);
    const previousSelectedModelRef = useRef<string | null>(null);
    const usageRecordedForRequestRef = useRef<string | null>(null);
    const activeConversationRef = useRef<Conversation | null>(null);
    const messagesRef = useRef<UIMessage[]>([]);
    const messageListRef = useRef<HTMLDivElement | null>(null);
    const shouldAutoScrollRef = useRef(true);
    const saveTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
    const saveQueueRef = useRef<Promise<void>>(Promise.resolve());
    const connectionSaveTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
    const skipInitialConnectionSaveRef = useRef(true);
    const cancelRequestedRef = useRef(false);
    const stopFallbackTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
    const copiedMessageTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
    const attachmentInputRef = useRef<HTMLInputElement | null>(null);
    const modelLoadPromiseRef = useRef<ReturnType<typeof ChatService.ListModels> | null>(null);
    const modelLoadSequenceRef = useRef(0);

    const selectedSavedConnectionProfile = useMemo(
        () => savedConnectionProfiles.find((profile) => profile.id === selectedSavedConnectionProfileID),
        [savedConnectionProfiles, selectedSavedConnectionProfileID],
    );
    const usingBuiltInConnectionProfile = Boolean(selectedSavedConnectionProfile?.isBuiltIn);
    const usingOpenRouter = isOpenRouterURL(baseURL);
    const selectedModelUsage = modelTokenUsage[selectedModel];
    const chatReasoningWarning = reasoningEffortWarning(reasoningEffort);

    const applyOpenRouterModelIDs = useCallback((modelIDs: string[]) => {
        const nextModelIDs = Array.from(new Set(modelIDs.map((modelID) => modelID.trim()).filter(Boolean)));
        setOpenRouterModelIDs(nextModelIDs);
        setSelectedModel((current) => nextModelIDs.includes(current) ? current : '');
    }, []);

    useEffect(() => {
        if (!usingOpenRouter || models.length === 0 || openRouterModelIDs.length === 0) return;
        setSelectedModel((current) => openRouterModelIDs.includes(current) ? current : '');
    }, [models.length, openRouterModelIDs, usingOpenRouter]);

    useEffect(() => {
        if (previousSelectedModelRef.current !== null && previousSelectedModelRef.current !== selectedModel) {
            setModelTokenUsage({});
        }
        previousSelectedModelRef.current = selectedModel;
    }, [selectedModel]);

    useEffect(() => {
        try {
            window.localStorage.setItem(benchmarkHistorySectionStateStorageKey, JSON.stringify(benchmarkHistorySectionOpen));
        } catch {
            // The sidebar remains usable when local storage is unavailable.
        }
    }, [benchmarkHistorySectionOpen]);

    const handleBenchmarkBusyChange = useCallback((nextBusy: boolean) => {
        setBenchmarkBusy(nextBusy);
    }, []);

    const handleBenchmarkSidebarChange = useCallback((nextState: ModelBenchmarkSidebarState) => {
        setBenchmarkSidebar(nextState);
    }, []);

    const openSyncWorkspace = useCallback(() => {
        setWorkspace('sync');
        setConnectionSettingsOpen(false);
    }, []);

    const handleBenchmarkOpenRequestHandled = useCallback(() => {
        setBenchmarkOpenRequestID(null);
    }, []);

    function toggleBenchmarkHistorySection(section: BenchmarkHistorySection) {
        setBenchmarkHistorySectionOpen((current) => ({...current, [section]: !current[section]}));
    }

    function requestBenchmarkDelete(summary: ModelBenchmarkSummary) {
        if (benchmarkBusy) return;
        setBenchmarkDeleteError('');
        setBenchmarkToDelete(summary);
    }

    async function confirmBenchmarkDelete() {
        const summary = benchmarkToDelete;
        if (!summary || deletingBenchmark) return;
        try {
            setDeletingBenchmark(true);
            setBenchmarkDeleteError('');
            await ChatService.DeleteModelBenchmark(summary.id);
            setBenchmarkSidebar((current) => ({
                ...current,
                recent: current.recent.filter((item) => item.id !== summary.id),
                imported: current.imported.filter((item) => item.id !== summary.id),
                synchronized: current.synchronized.filter((item) => item.id !== summary.id),
            }));
            setBenchmarkOpenRequestID((current) => current === summary.id ? null : current);
            setBenchmarkHistoryRefreshKey((current) => current + 1);
            setBenchmarkToDelete(null);
        } catch (reason) {
            setBenchmarkDeleteError(reason instanceof Error ? reason.message : String(reason));
        } finally {
            setDeletingBenchmark(false);
        }
    }

    function replaceMessages(nextMessages: UIMessage[]) {
        messagesRef.current = nextMessages;
        setMessages(nextMessages);
    }

    function activateConversation(conversation: Conversation) {
        activeConversationRef.current = conversation;
        setActiveConversation(conversation);
        setRenamingConversation(false);
        setConversationTitleDraft('');
        setAttachments([]);
        if (attachmentInputRef.current) {
            attachmentInputRef.current.value = '';
        }
        replaceMessages((conversation.messages || []).map(toUIMessage));
        shouldAutoScrollRef.current = true;
        setCanScrollUp(false);
        setShowScrollToLatest(false);
    }

    function upsertConversationSummary(conversation: Conversation) {
        const summary = summaryFromConversation(conversation);
        setConversations((current) => sortConversations([
            summary,
            ...current.filter((item) => item.id !== summary.id),
        ]));
    }

    async function saveMessages(nextMessages: UIMessage[], source = activeConversationRef.current) {
        if (!source) {
            return;
        }
        const saved = await ChatService.SaveConversation({...source, messages: toStoredMessages(nextMessages)});
        activeConversationRef.current = saved;
        setActiveConversation(saved);
        upsertConversationSummary(saved);
    }

    function persistMessages(nextMessages: UIMessage[]) {
        const pendingSave = saveQueueRef.current.then(() => saveMessages(nextMessages));
        saveQueueRef.current = pendingSave.catch(() => undefined);
        void pendingSave.catch((reason) => setError(String(reason)));
    }

    function scheduleMessageSave(nextMessages: UIMessage[]) {
        if (saveTimerRef.current) {
            clearTimeout(saveTimerRef.current);
        }
        saveTimerRef.current = setTimeout(() => {
            saveTimerRef.current = null;
            persistMessages(nextMessages);
        }, 400);
    }

    async function createConversation() {
        if (busy) {
            return;
        }
        try {
            setError('');
            const conversation = await ChatService.CreateConversation();
            activateConversation(conversation);
            upsertConversationSummary(conversation);
        } catch (reason) {
            setError(String(reason));
        }
    }

    async function openConversation(id: string) {
        if (busy || id === activeConversationRef.current?.id) {
            return;
        }
        try {
            setError('');
            const conversation = await ChatService.OpenConversation(id);
            activateConversation(conversation);
        } catch (reason) {
            setError(String(reason));
        }
    }

    function requestConversationDelete() {
        const conversation = activeConversationRef.current;
        if (!conversation || busy) {
            return;
        }
        setConversationToDelete(conversation);
    }

    async function confirmConversationDelete() {
        const conversation = conversationToDelete;
        if (!conversation || busy || deletingConversation) {
            return;
        }

        try {
            setDeletingConversation(true);
            setError('');
            await ChatService.DeleteConversation(conversation.id);
            setConversations((current) => current.filter((item) => item.id !== conversation.id));
            if (activeConversationRef.current?.id === conversation.id) {
                activeConversationRef.current = null;
                setActiveConversation(null);
                replaceMessages([]);
            }
            setInput('');
            setAttachments([]);
            if (attachmentInputRef.current) {
                attachmentInputRef.current.value = '';
            }
            shouldAutoScrollRef.current = true;
            setCanScrollUp(false);
            setShowScrollToLatest(false);
            setConversationToDelete(null);
        } catch (reason) {
            setError(String(reason));
        } finally {
            setDeletingConversation(false);
        }
    }

    function beginConversationRename() {
        const conversation = activeConversationRef.current;
        if (!conversation || busy) {
            return;
        }
        setConversationTitleDraft(conversation.title);
        setRenamingConversation(true);
    }

    async function saveConversationTitle() {
        const conversation = activeConversationRef.current;
        const title = conversationTitleDraft.trim();
        if (!conversation || busy || !title) {
            if (!title) {
                setError('대화 이름을 입력해 주세요.');
            }
            return;
        }

        try {
            setError('');
            const saved = await ChatService.SaveConversation({
                ...conversation,
                title,
                messages: toStoredMessages(messagesRef.current),
            });
            activeConversationRef.current = saved;
            setActiveConversation(saved);
            upsertConversationSummary(saved);
            setRenamingConversation(false);
        } catch (reason) {
            setError(String(reason));
        }
    }

    function updateBaseURL(value: string) {
        setBaseURL(value);
        setModels([]);
        setSelectedModel('');
        setOpenRouterModelPickerOpen(false);
        setModelTokenUsage({});
        setConnectionMessage('서버 주소가 변경되었습니다. 모델을 다시 불러와 주세요');
    }

    function selectSavedConnectionProfile(id: string) {
        const profile = savedConnectionProfiles.find((item) => item.id === id);
        setSelectedSavedConnectionProfileID(id);
        if (!profile) {
            setConnectionProfileName('');
            return;
        }

        setConnectionProfileName(profile.name);
        updateBaseURL(profile.baseURL);
        setAPIKey('');
        setConnectionMessage(profile.isBuiltIn
            ? 'OpenRouter 기본 프로필을 선택했습니다. API 키를 입력한 뒤 모델을 불러와 주세요'
            : '프로필을 선택했습니다. API 키를 입력한 뒤 모델을 불러와 주세요');
    }

    async function saveNamedConnectionProfile() {
        if (busy || savingConnectionProfile || usingBuiltInConnectionProfile) {
            return;
        }
        if (!canSaveConnectionProfile(baseURL)) {
            setError('저장할 수 있는 서버 URL을 입력해 주세요.');
            return;
        }

        try {
            setSavingConnectionProfile(true);
            setError('');
            const saved = await ChatService.SaveNamedConnectionProfile({
                id: selectedSavedConnectionProfileID,
                name: connectionProfileName,
                baseURL,
            });
            setSavedConnectionProfiles((current) => sortSavedConnectionProfiles([
                ...current.filter((item) => item.id !== saved.id),
                saved,
            ]));
            setSelectedSavedConnectionProfileID(saved.id);
            setConnectionProfileName(saved.name);
            setConnectionMessage('연결 프로필을 저장했습니다');
        } catch (reason) {
            setError(String(reason));
        } finally {
            setSavingConnectionProfile(false);
        }
    }

    function requestSavedConnectionProfileDelete() {
        const profile = savedConnectionProfiles.find((item) => item.id === selectedSavedConnectionProfileID);
        if (!profile || profile.isBuiltIn || busy || savingConnectionProfile) {
            return;
        }
        setConnectionProfileToDelete(profile);
    }

    async function confirmSavedConnectionProfileDelete() {
        const profile = connectionProfileToDelete;
        if (!profile || deletingConnectionProfile) {
            return;
        }
        try {
            setDeletingConnectionProfile(true);
            setError('');
            await ChatService.DeleteSavedConnectionProfile(profile.id);
            setSavedConnectionProfiles((current) => current.filter((item) => item.id !== profile.id));
            if (selectedSavedConnectionProfileID === profile.id) {
                setSelectedSavedConnectionProfileID('');
                setConnectionProfileName('');
            }
            setConnectionMessage('연결 프로필을 삭제했습니다');
            setConnectionProfileToDelete(null);
        } catch (reason) {
            setError(String(reason));
        } finally {
            setDeletingConnectionProfile(false);
        }
    }

    useEffect(() => {
        let cancelled = false;

        async function loadConversations() {
            try {
                const loaded = await ChatService.ListConversations();
                if (cancelled) {
                    return;
                }
                const nextConversations = sortConversations(loaded || []);
                setConversations(nextConversations);
            } catch (reason) {
                if (!cancelled) {
                    setError(String(reason));
                }
            } finally {
                if (!cancelled) {
                    setLoadingConversations(false);
                }
            }
        }

        void loadConversations();
        return () => {
            cancelled = true;
        };
    }, []);

    useEffect(() => {
        let cancelled = false;

        async function loadConnectionProfiles() {
            try {
                const [profile, profiles] = await Promise.all([
                    ChatService.LoadConnectionProfile(),
                    ChatService.ListSavedConnectionProfiles(),
                ]);
                if (cancelled) {
                    return;
                }
                setSavedConnectionProfiles(withBuiltInConnectionProfiles(profiles || []));
                if (profile.baseURL) {
                    setBaseURL(profile.baseURL);
                    setConnectionMessage('저장된 서버 주소를 불러왔습니다. 모델을 불러와 선택해 주세요');
                }
            } catch (reason) {
                if (!cancelled) {
                    setError(String(reason));
                }
            } finally {
                if (!cancelled) {
                    setConnectionProfileReady(true);
                }
            }
        }

        void loadConnectionProfiles();
        return () => {
            cancelled = true;
        };
    }, []);

    useEffect(() => {
        if (!connectionProfileReady) {
            return;
        }
        if (skipInitialConnectionSaveRef.current) {
            skipInitialConnectionSaveRef.current = false;
            return;
        }
        if (!canSaveConnectionProfile(baseURL)) {
            return;
        }
        if (connectionSaveTimerRef.current) {
            clearTimeout(connectionSaveTimerRef.current);
        }
        const profile: SavedConnectionProfile = {id: '', name: '', baseURL};
        connectionSaveTimerRef.current = setTimeout(() => {
            connectionSaveTimerRef.current = null;
            void ChatService.SaveConnectionProfile(profile).catch((reason) => setError(String(reason)));
        }, 400);
    }, [baseURL, connectionProfileReady]);

    useEffect(() => {
        const cancelListener = Events.On('chat:event', (event) => {
            const payload: ChatEvent = event.data;
            if (payload.requestID !== activeRequestRef.current) {
                return;
            }
            const assistantID = assistantMessageRef.current;
            if (!assistantID) {
                return;
            }

            const delta = payload.delta;
            if (payload.type === 'delta' && delta) {
                if (cancelRequestedRef.current) {
                    return;
                }
                replaceMessages(messagesRef.current.map((message) =>
                    message.id === assistantID
                        ? {...message, content: appendAssistantDelta(message.content, delta)}
                        : message,
                ));
                scheduleMessageSave(messagesRef.current);
                return;
            }

            const usage = payload.usage;
            if (payload.type === 'usage' && usage) {
                replaceMessages(messagesRef.current.map((message) =>
                    message.id === assistantID ? {...message, usage} : message,
                ));
                if (usageRecordedForRequestRef.current !== payload.requestID) {
                    const model = activeRequestModelRef.current;
                    if (model) {
                        setModelTokenUsage((current) => {
                            const previous = current[model] || {promptTokens: 0, completionTokens: 0, totalTokens: 0};
                            return {
                                ...current,
                                [model]: {
                                    promptTokens: previous.promptTokens + usage.promptTokens,
                                    completionTokens: previous.completionTokens + usage.completionTokens,
                                    totalTokens: previous.totalTokens + usage.totalTokens,
                                },
                            };
                        });
                    }
                    usageRecordedForRequestRef.current = payload.requestID;
                }
                return;
            }

            if (payload.type === 'completed') {
                finishRequest(assistantID, 'complete', payload.metrics);
            } else if (payload.type === 'cancelled') {
                finishRequest(assistantID, 'cancelled', payload.metrics);
            } else if (payload.type === 'failed') {
                setError(payload.error || '응답 생성 중 오류가 발생했습니다.');
                finishRequest(assistantID, 'failed', payload.metrics);
            }
        });
        return cancelListener;
    }, []);

    useEffect(() => () => {
        if (saveTimerRef.current) {
            clearTimeout(saveTimerRef.current);
        }
        if (connectionSaveTimerRef.current) {
            clearTimeout(connectionSaveTimerRef.current);
        }
        if (stopFallbackTimerRef.current) {
            clearTimeout(stopFallbackTimerRef.current);
        }
        if (copiedMessageTimerRef.current) {
            clearTimeout(copiedMessageTimerRef.current);
        }
    }, []);

    useEffect(() => {
        const element = messageListRef.current;
        if (element && shouldAutoScrollRef.current) {
            element.scrollTop = element.scrollHeight;
            setCanScrollUp(element.scrollTop > 1);
            setShowScrollToLatest(false);
        }
    }, [messages]);

    const visibleConversations = useMemo(() => {
        const query = conversationSearch.trim().toLocaleLowerCase('ko-KR');
        if (!query) {
            return conversations;
        }
        return conversations.filter((conversation) => conversation.title.toLocaleLowerCase('ko-KR').includes(query));
    }, [conversationSearch, conversations]);

    const canSend = useMemo(
        () => (input.trim() !== '' || attachments.length > 0) && selectedModel !== '' && activeConversation !== null && !busy,
        [input, attachments, selectedModel, activeConversation, busy],
    );

    function finishRequest(assistantID: string, status: MessageStatus, metrics?: ResponseMetrics | null) {
        if (assistantMessageRef.current !== assistantID) {
            return;
        }
        const nextMessages = messagesRef.current.map((message) =>
            message.id === assistantID ? {...message, status, metrics: metrics ?? message.metrics} : message,
        );
        replaceMessages(nextMessages);
        activeRequestRef.current = null;
        assistantMessageRef.current = null;
        activeRequestModelRef.current = null;
        usageRecordedForRequestRef.current = null;
        cancelRequestedRef.current = false;
        setCancelling(false);
        if (stopFallbackTimerRef.current) {
            clearTimeout(stopFallbackTimerRef.current);
            stopFallbackTimerRef.current = null;
        }
        setBusy(false);
        if (saveTimerRef.current) {
            clearTimeout(saveTimerRef.current);
            saveTimerRef.current = null;
        }
        persistMessages(nextMessages);
    }

    function updateScrollState(element: HTMLDivElement) {
        const distanceFromBottom = element.scrollHeight - element.scrollTop - element.clientHeight;
        const isNearBottom = distanceFromBottom <= 48;
        shouldAutoScrollRef.current = isNearBottom;
        setCanScrollUp(element.scrollTop > 1);
        setShowScrollToLatest(!isNearBottom);
    }

    function handleMessageListScroll() {
        const element = messageListRef.current;
        if (element) {
            updateScrollState(element);
        }
    }

    function handleChatPanelWheel(event: WheelEvent<HTMLElement>) {
        const element = messageListRef.current;
        if (!element || event.deltaY === 0) {
            return;
        }

        const maximumScrollTop = Math.max(0, element.scrollHeight - element.clientHeight);
        const nextScrollTop = Math.min(maximumScrollTop, Math.max(0, element.scrollTop + event.deltaY));
        if (nextScrollTop === element.scrollTop) {
            return;
        }

        event.preventDefault();
        element.scrollTop = nextScrollTop;
        updateScrollState(element);
    }

    function scrollToPrevious() {
        const element = messageListRef.current;
        if (!element) {
            return;
        }

        shouldAutoScrollRef.current = false;
        element.scrollTop = Math.max(0, element.scrollTop - element.clientHeight * 0.8);
        updateScrollState(element);
    }

    function scrollToLatest() {
        const element = messageListRef.current;
        if (!element) {
            return;
        }

        shouldAutoScrollRef.current = true;
        element.scrollTop = element.scrollHeight;
        setShowScrollToLatest(false);
    }

    async function loadModels(): Promise<ModelOption[]> {
        if (loadingModels) return [];
        const operationID = ++modelLoadSequenceRef.current;
        setLoadingModels(true);
        setError('');
        setConnectionMessage('연결 확인 중…');
        try {
            const request = ChatService.ListModels({baseURL, apiKey});
            modelLoadPromiseRef.current = request;
            const result = await request;
            if (operationID !== modelLoadSequenceRef.current) return [];
            const nextModels = (result || []) as ModelOption[];
            setModels(nextModels);
            setModelTokenUsage({});
            if (isOpenRouterURL(baseURL)) {
                setSelectedModel((current) => openRouterModelIDs.includes(current) ? current : '');
            } else {
                setSelectedModel((current) =>
                    nextModels.some((model) => model.id === current) ? current : (nextModels[0]?.id || ''),
                );
            }
            setConnectionMessage(nextModels.length > 0 ? `${nextModels.length}개 모델 연결됨` : '사용 가능한 모델 없음');
            return nextModels;
        } catch (reason) {
            if (operationID !== modelLoadSequenceRef.current) return [];
            setModels([]);
            setSelectedModel('');
            setConnectionMessage('연결 실패');
            setError(String(reason));
            return [];
        } finally {
            if (operationID === modelLoadSequenceRef.current) {
                modelLoadPromiseRef.current = null;
                setLoadingModels(false);
            }
        }
    }

    function cancelModelLoad() {
        const request = modelLoadPromiseRef.current;
        if (!request) return;
        modelLoadSequenceRef.current += 1;
        modelLoadPromiseRef.current = null;
        setLoadingModels(false);
        setConnectionMessage('모델 불러오기를 취소했습니다');
        void request.cancel('사용자가 모델 목록 불러오기를 취소했습니다');
    }

    async function openOpenRouterModelPicker() {
        if (busy || loadingModels) return;
        const availableModels = models.length > 0 ? models : await loadModels();
        if (availableModels.length > 0) {
            setOpenRouterModelPickerOpen(true);
        }
    }

    async function beginAssistantResponse(
        conversation: Conversation,
        assistantMessage: UIMessage,
        nextMessages: UIMessage[],
        requestMessages: UIMessage[],
    ) {
        const requestID = makeID();
        activeRequestRef.current = requestID;
        assistantMessageRef.current = assistantMessage.id;
        activeRequestModelRef.current = selectedModel;
        usageRecordedForRequestRef.current = null;
        cancelRequestedRef.current = false;
        setCancelling(false);
        shouldAutoScrollRef.current = true;
        setShowScrollToLatest(false);
        replaceMessages(nextMessages);
        setBusy(true);
        setError('');

        try {
            await saveMessages(nextMessages, conversation);
            const request: ChatRequest = {
                requestID,
                profile: {baseURL, apiKey},
                model: selectedModel,
                messages: messagesForModel(requestMessages, includeConversationHistory),
                reasoningEffort,
                benchmark: false,
            };
            await ChatService.StartChat(request);
        } catch (reason) {
            setError(String(reason));
            finishRequest(assistantMessage.id, 'failed');
        }
    }

    async function sendMessage(event?: FormEvent) {
        event?.preventDefault();
        const text = input.trim();
        const conversation = activeConversationRef.current;
        if ((!text && attachments.length === 0) || !selectedModel || busy || !conversation) {
            return;
        }

        const attachmentBudget = Math.min(
            maxAttachmentContentSize,
            Math.floor(maxAttachmentTotalContentSize / Math.max(attachments.length, 1)),
        );
        const selectedAttachments: ChatAttachment[] = attachments.map((attachment) => ({
            name: attachment.name,
            size: attachment.size,
            ...selectAttachmentContent(attachment.source, attachment.chunks, text, attachmentBudget),
        }));
        const userMessage: UIMessage = {id: makeID(), role: 'user', content: text, status: 'complete', attachments: selectedAttachments};
        const assistantMessage: UIMessage = {id: makeID(), role: 'assistant', content: '', status: 'streaming', model: selectedModel, attachments: []};
        const nextMessages = [...messagesRef.current, userMessage, assistantMessage];
        setInput('');
        setAttachments([]);
        if (attachmentInputRef.current) {
            attachmentInputRef.current.value = '';
        }
        await beginAssistantResponse(conversation, assistantMessage, nextMessages, nextMessages);
    }

    async function addAttachments(event: ChangeEvent<HTMLInputElement>) {
        const files = Array.from(event.target.files || []);
        event.target.value = '';
        if (files.length === 0) return;
        if (attachments.length + files.length > maxAttachmentsPerMessage) {
            setError(`파일은 한 메시지에 최대 ${maxAttachmentsPerMessage}개까지 첨부할 수 있습니다.`);
            return;
        }

        try {
            const nextAttachments = await Promise.all(files.map(async (file): Promise<AttachmentDraft> => {
                const name = attachmentFileName(file.name);
                if (!attachmentFileExtensions.has(attachmentExtension(name))) {
                    throw new Error(`“${name}”은 아직 지원하지 않는 파일 형식입니다.`);
                }
                if (file.size > maxAttachmentFileSize) {
                    throw new Error(`“${name}”은 파일당 ${formatFileSize(maxAttachmentFileSize)}까지 첨부할 수 있습니다.`);
                }

                const extracted = await readAttachmentContent(file, name);
                return {name, size: file.size, ...extracted};
            }));
            const currentFileSize = attachments.reduce((total, attachment) => total + attachment.size, 0);
            const nextFileSize = nextAttachments.reduce((total, attachment) => total + attachment.size, currentFileSize);
            if (nextFileSize > maxAttachmentTotalFileSize) {
                setError(`첨부 원본 파일의 전체 크기는 ${formatFileSize(maxAttachmentTotalFileSize)}까지 첨부할 수 있습니다.`);
                return;
            }
            setAttachments((current) => [...current, ...nextAttachments]);
            setError('');
        } catch (reason) {
            setError(reason instanceof Error ? reason.message : String(reason));
        }
    }

    function removeAttachment(index: number) {
        setAttachments((current) => current.filter((_, attachmentIndex) => attachmentIndex !== index));
    }

    async function regenerateResponse(messageID: string) {
        const conversation = activeConversationRef.current;
        const messageIndex = messagesRef.current.findIndex((message) => message.id === messageID);
        if (!conversation || !selectedModel || busy || messageIndex < 1 || messageIndex !== messagesRef.current.length - 1) {
            return;
        }

        const previousMessages = messagesRef.current.slice(0, messageIndex);
        const previousMessage = previousMessages[previousMessages.length - 1];
        if (!previousMessage || previousMessage.role !== 'user') {
            return;
        }

        const assistantMessage: UIMessage = {id: messageID, role: 'assistant', content: '', status: 'streaming', model: selectedModel, attachments: []};
        await beginAssistantResponse(conversation, assistantMessage, [...previousMessages, assistantMessage], previousMessages);
    }

    async function copyMessage(message: UIMessage) {
        if (!message.content) {
            return;
        }
        try {
            if (navigator.clipboard?.writeText) {
                await navigator.clipboard.writeText(message.content);
            } else {
                const temporary = document.createElement('textarea');
                temporary.value = message.content;
                temporary.style.position = 'fixed';
                temporary.style.opacity = '0';
                document.body.append(temporary);
                temporary.select();
                const copied = document.execCommand('copy');
                temporary.remove();
                if (!copied) {
                    throw new Error('클립보드에 복사할 수 없습니다.');
                }
            }
            setCopiedMessageID(message.id);
            if (copiedMessageTimerRef.current) {
                clearTimeout(copiedMessageTimerRef.current);
            }
            copiedMessageTimerRef.current = setTimeout(() => {
                copiedMessageTimerRef.current = null;
                setCopiedMessageID(null);
            }, 1_800);
        } catch (reason) {
            setError(String(reason));
        }
    }

    function requestChatShare(messageID: string) {
        const answerIndex = messagesRef.current.findIndex((message) => message.id === messageID);
        const answer = answerIndex >= 0 ? messagesRef.current[answerIndex] : null;
        if (!answer || answer.role !== 'assistant' || !answer.content) return;
        const question = messagesRef.current.slice(0, answerIndex).reverse().find((message) => message.role === 'user') || null;
        setShareTarget({answer, question});
        setShareContentMode(question ? 'question-answer' : 'answer');
        setShareError('');
    }

    async function saveChatShare(format: ChatShareFormat) {
        const target = shareTarget;
        if (!target || sharingFormat) return;
        const extension = format === 'html' ? 'html' : 'md';
        const formatLabel = format === 'html' ? 'HTML' : 'Markdown';
        try {
            setSharingFormat(format);
            setShareError('');
            const path = await Dialogs.SaveFile({
                Title: `${formatLabel} 공유 파일 저장`,
                ButtonText: '저장',
                Filename: chatShareFilename(shareContentMode, format),
                Filters: [{DisplayName: `${formatLabel} 파일`, Pattern: `*.${extension}`}],
            });
            if (!path) return;
            const contents = format === 'html'
                ? chatShareHTML(target, shareContentMode)
                : chatShareMarkdown(target, shareContentMode);
            await ChatService.SaveChatShare(path, contents);
            setShareTarget(null);
        } catch (reason) {
            setShareError(reason instanceof Error ? reason.message : String(reason));
        } finally {
            setSharingFormat(null);
        }
    }

    async function stopGeneration() {
        const requestID = activeRequestRef.current;
        const assistantID = assistantMessageRef.current;
        if (!requestID || !assistantID || cancelling) {
            return;
        }

        try {
            cancelRequestedRef.current = true;
            setCancelling(true);
            await ChatService.CancelChat(requestID);
            if (activeRequestRef.current !== requestID || assistantMessageRef.current !== assistantID) {
                return;
            }
            if (stopFallbackTimerRef.current) {
                clearTimeout(stopFallbackTimerRef.current);
            }
            stopFallbackTimerRef.current = setTimeout(() => {
                if (activeRequestRef.current === requestID && assistantMessageRef.current === assistantID) {
                    finishRequest(assistantID, 'cancelled');
                }
            }, 2_000);
        } catch (reason) {
            cancelRequestedRef.current = false;
            setCancelling(false);
            setError(String(reason));
        }
    }

    function handleComposerKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
        if (event.key === 'Enter' && !event.shiftKey) {
            event.preventDefault();
            void sendMessage();
        }
    }

    function handleAPIKeyKeyDown(event: KeyboardEvent<HTMLInputElement>) {
        if (event.key !== 'Enter') return;
        event.preventDefault();
        if (!busy && !loadingModels) {
            void loadModels();
        }
    }

    function renderConnectionSettings() {
        return (
            <main className="connection-panel" aria-label="연결 설정">
                <header className="connection-panel-header">
                    <div>
                        <span className="eyebrow">CONNECTION SETTINGS</span>
                        <h1>모델 연결 설정</h1>
                        <p>연결 프로필, API 키, 사용할 모델을 이 화면에서 관리합니다.</p>
                    </div>
                    <button className="secondary-button connection-return-button" type="button" onClick={() => setConnectionSettingsOpen(false)} disabled={busy}>
                        홈으로 돌아가기
                    </button>
                </header>
                {error && <div className="error-banner" role="alert">{error}</div>}
                <div className="connection-settings-grid">
                    <section className="connection-settings-card">
                        <div className="connection-settings-card-heading">
                            <span>1</span>
                            <div>
                                <h2>연결 프로필</h2>
                                <p>서버 주소는 프로필로 저장할 수 있고 API 키는 저장되지 않습니다.</p>
                            </div>
                        </div>
                        <label>
                            연결 프로필 <small>API 키 제외</small>
                            <select
                                value={selectedSavedConnectionProfileID}
                                onChange={(event) => selectSavedConnectionProfile(event.target.value)}
                                disabled={busy || savingConnectionProfile}
                            >
                                <option value="">새 연결 프로필</option>
                                {savedConnectionProfiles.filter((profile) => profile.isBuiltIn).map((profile) => (
                                    <option key={profile.id} value={profile.id}>{profile.name} · 기본</option>
                                ))}
                                <optgroup label="저장된 프로필">
                                    {savedConnectionProfiles.filter((profile) => !profile.isBuiltIn).map((profile) => (
                                        <option key={profile.id} value={profile.id}>{profile.name}</option>
                                    ))}
                                </optgroup>
                            </select>
                        </label>
                        <label>
                            프로필 이름
                            <input
                                value={connectionProfileName}
                                onChange={(event) => setConnectionProfileName(event.target.value)}
                                placeholder="예: 로컬 vLLM"
                                disabled={busy || savingConnectionProfile || usingBuiltInConnectionProfile}
                            />
                        </label>
                        <label>
                            서버 URL
                            <input
                                value={baseURL}
                                onChange={(event) => updateBaseURL(event.target.value)}
                                placeholder="http://localhost:8000"
                                spellCheck={false}
                                disabled={busy || savingConnectionProfile || usingBuiltInConnectionProfile}
                            />
                        </label>
                        {!usingBuiltInConnectionProfile && (
                            <button
                                className={`profile-save-button ${selectedSavedConnectionProfileID ? 'update' : 'create'}`}
                                type="button"
                                onClick={() => void saveNamedConnectionProfile()}
                                disabled={busy || savingConnectionProfile || !connectionProfileName.trim()}
                            >
                                {savingConnectionProfile ? '저장 중…' : selectedSavedConnectionProfileID ? '프로필 업데이트' : '프로필 저장'}
                            </button>
                        )}
                        {selectedSavedConnectionProfileID && !usingBuiltInConnectionProfile && (
                            <button
                                className="profile-delete-button"
                                type="button"
                                onClick={requestSavedConnectionProfileDelete}
                                disabled={busy || savingConnectionProfile}
                            >
                                프로필 삭제
                            </button>
                        )}
                    </section>
                    <section className="connection-settings-card">
                        <div className="connection-settings-card-heading">
                            <span>2</span>
                            <div>
                                <h2>모델 선택</h2>
                                <p>현재 연결에서 사용할 모델을 불러와 선택합니다.</p>
                            </div>
                        </div>
                        <label>
                            API 키 <small>{usingBuiltInConnectionProfile ? 'OpenRouter · 저장 안 됨' : '선택'}</small>
                            <input
                                value={apiKey}
                                onChange={(event) => setAPIKey(event.target.value)}
                                onKeyDown={handleAPIKeyKeyDown}
                                placeholder={usingBuiltInConnectionProfile ? 'OpenRouter API 키 입력' : '필요한 경우 입력'}
                                type="password"
                                autoComplete="off"
                                disabled={busy}
                            />
                        </label>
                        <div className="connection-model-actions">
                            <button className="secondary-button" type="button" onClick={loadModels} disabled={loadingModels || busy}>
                                {loadingModels ? '모델 불러오는 중…' : '모델 불러오기'}
                            </button>
                            {loadingModels && <button className="model-load-cancel-button" type="button" onClick={cancelModelLoad}>취소</button>}
                        </div>
                        <p className="connection-message">{connectionMessage}</p>
                        <label>
                            <span>사용할 모델</span>
                            {usingOpenRouter ? (
                                <div className="openrouter-model-selection">
                                    <select value={selectedModel} onChange={(event) => setSelectedModel(event.target.value)} disabled={!openRouterModelIDs.length || busy}>
                                        <option value="">모델을 선택하세요</option>
                                        {openRouterModelIDs.map((modelID) => <option key={modelID} value={modelID}>{modelID}</option>)}
                                    </select>
                                    <button type="button" onClick={() => void openOpenRouterModelPicker()} disabled={busy || loadingModels}>
                                        {loadingModels ? '모델 불러오는 중…' : '모델 관리'}
                                    </button>
                                    <small>{openRouterModelIDs.length}개 선택됨</small>
                                </div>
                            ) : (
                                <select value={selectedModel} onChange={(event) => setSelectedModel(event.target.value)} disabled={!models.length || busy}>
                                    {!models.length && <option value="">모델을 불러와 주세요</option>}
                                    {models.map((model) => <option key={model.id} value={model.id}>{model.id}</option>)}
                                </select>
                            )}
                        </label>
                        <p className="privacy-note">OpenRouter 기본 프로필은 서버 URL만 미리 설정합니다. 다른 프로필도 이름과 서버 URL만 저장하며, 모델과 API 키는 저장하지 않습니다.</p>
                    </section>
                </div>
            </main>
        );
    }

    return (
        <div className={`app-shell ${sidebarVisible ? '' : 'sidebar-collapsed'}`}>
            <aside className="sidebar">
                <div className="sidebar-header">
                    <div className="brand">
                        <span className="brand-mark" aria-hidden="true" />
                        <div>
                            <strong>Agent Chat</strong>
                            <span>Local AI desktop</span>
                        </div>
                    </div>
                    <button
                        className="sidebar-visibility-button"
                        type="button"
                        onClick={() => setSidebarVisible(false)}
                        aria-label="사이드바 숨기기"
                        title="사이드바 숨기기"
                    >
                        <span aria-hidden="true">‹</span>
                    </button>
                </div>

                <nav className="workspace-switch" aria-label="작업 공간">
                    <button
                        className={workspace === 'chat' ? 'active' : ''}
                        type="button"
                        onClick={() => {
                            setWorkspace('chat');
                            setConnectionSettingsOpen(false);
                        }}
                        disabled={busy || benchmarkBusy}
                    >
                        채팅
                    </button>
                    <button
                        className={workspace === 'benchmark' ? 'active' : ''}
                        type="button"
                        onClick={() => {
                            setWorkspace('benchmark');
                            setConnectionSettingsOpen(false);
                        }}
                        disabled={busy || benchmarkBusy}
                    >
                        모델 실험실
                    </button>
                    <button
                        className={`sync-workspace-button ${workspace === 'sync' ? 'active' : ''}`}
                        type="button"
                        onClick={openSyncWorkspace}
                        disabled={busy || benchmarkBusy}
                    >
                        <span>결과 동기화</span>
                    </button>
                </nav>

                {workspace === 'chat' ? (
                    <>
                <section className={`conversations ${conversationsExpanded ? '' : 'collapsed'}`} aria-label="대화">
                    <div className="section-heading">
                        <button
                            className="section-toggle"
                            type="button"
                            onClick={() => setConversationsExpanded((current) => !current)}
                            aria-expanded={conversationsExpanded}
                            aria-controls="conversation-content"
                        >
                            <span>대화</span>
                            <span className="section-toggle-indicator" aria-hidden="true">⌄</span>
                        </button>
                        <button className="new-conversation-button" type="button" onClick={() => void createConversation()} disabled={busy}>
                            + 새 대화
                        </button>
                    </div>
                    {conversationsExpanded && (
                        <div className="collapsible-content" id="conversation-content">
                            <input
                                className="conversation-search"
                                value={conversationSearch}
                                onChange={(event) => setConversationSearch(event.target.value)}
                                placeholder="대화 검색"
                                type="search"
                            />
                            <div className="conversation-list">
                                {loadingConversations && <p className="conversation-empty">대화 불러오는 중…</p>}
                                {!loadingConversations && visibleConversations.map((conversation) => (
                                    <button
                                        className={`conversation-item ${conversation.id === activeConversation?.id ? 'active' : ''}`}
                                        key={conversation.id}
                                        type="button"
                                        onClick={() => void openConversation(conversation.id)}
                                        disabled={busy}
                                    >
                                        <span>{conversation.title}</span>
                                        <small>{conversation.messageCount}개 메시지 · {formatUpdatedAt(conversation.updatedAt)}</small>
                                    </button>
                                ))}
                                {!loadingConversations && conversations.length > 0 && visibleConversations.length === 0 && (
                                    <p className="conversation-empty">검색 결과가 없습니다.</p>
                                )}
                            </div>
                        </div>
                    )}
                </section>

                <section className="sidebar-chat-settings" aria-label="채팅 설정">
                    <button
                        className={`sidebar-connection-button ${connectionSettingsOpen ? 'active' : ''}`}
                        type="button"
                        onClick={() => setConnectionSettingsOpen(true)}
                        disabled={busy}
                    >
                        <span className={`connection-dot ${models.length ? 'online' : ''}`} />
                        <span>연결 설정</span>
                        <small title={selectedModel}>{selectedModel || '모델을 선택하세요'}</small>
                    </button>
                    <div className="sidebar-history-setting">
                        <label className="sidebar-history-toggle">
                            <span>이전 대화 포함</span>
                            <input
                                type="checkbox"
                                role="switch"
                                checked={includeConversationHistory}
                                onChange={(event) => setIncludeConversationHistory(event.target.checked)}
                                disabled={busy}
                                aria-describedby="chat-history-description"
                            />
                            <span className="sidebar-switch-track" aria-hidden="true" />
                        </label>
                        <p id="chat-history-description">
                            {includeConversationHistory
                                ? '이 대화의 이전 질문·답변과 첨부 내용도 전송합니다.'
                                : '현재 질문과 첨부만 전송합니다.'}
                        </p>
                    </div>
                </section>
                    </>
                ) : workspace === 'benchmark' ? (
                    <section className="benchmark-sidebar">
                        <div className="section-heading"><span>모델 실험실</span></div>
                        {benchmarkSidebar.model ? (
                            <section className="benchmark-sidebar-current">
                                <span className={benchmarkSidebar.status}>실행 중</span>
                                <strong>{benchmarkSidebar.model}</strong>
                                <small title={benchmarkSidebar.profileBaseURL}>{benchmarkSidebar.profileName} · {benchmarkSidebar.profileBaseURL}</small>
                                <small>{benchmarkSidebar.completedCaseCount}/{benchmarkSidebar.caseCount}개 완료</small>
                            </section>
                        ) : (
                            <p>저장된 프로필에서 모델 하나를 선택해 편집 가능한 4개 테스트를 순차 실행합니다.</p>
                        )}
                        <div className="benchmark-sidebar-history-groups">
                            <section className={`benchmark-sidebar-history ${benchmarkHistorySectionOpen.recent ? 'expanded' : 'collapsed'}`} aria-label="내 벤치마크">
                                <button
                                    className="benchmark-sidebar-history-toggle"
                                    type="button"
                                    aria-expanded={benchmarkHistorySectionOpen.recent}
                                    aria-controls="benchmark-sidebar-recent-list"
                                    onClick={() => toggleBenchmarkHistorySection('recent')}
                                >
                                    <span aria-hidden="true">{benchmarkHistorySectionOpen.recent ? '⌄' : '›'}</span>
                                    <strong>내 벤치마크</strong>
                                    <small>{benchmarkSidebar.recent.length}</small>
                                </button>
                                {benchmarkHistorySectionOpen.recent && <div className="benchmark-sidebar-history-list" id="benchmark-sidebar-recent-list">
                                    {benchmarkSidebar.isHistoryLoading && <small>기록을 불러오는 중…</small>}
                                    {!benchmarkSidebar.isHistoryLoading && benchmarkSidebar.recent.length === 0 && <small>아직 실행한 벤치마크가 없습니다.</small>}
                                    {benchmarkSidebar.recent.map((item) => (
                                        <button
                                            className="benchmark-sidebar-history-item"
                                            key={item.id}
                                            type="button"
                                            disabled={benchmarkBusy}
                                            onClick={() => setBenchmarkOpenRequestID(item.id)}
                                            title={`${item.profileName} · ${item.model} · ${item.profileBaseURL}`}
                                        >
                                            <strong>{item.profileName} · {item.model}</strong>
                                            <span>{item.profileBaseURL}</span>
                                            <small>{item.suiteName} · {item.completedCaseCount}/{item.caseCount}개 · {formatUpdatedAt(item.updatedAt)}</small>
                                        </button>
                                    ))}
                                </div>}
                            </section>
                            <section className={`benchmark-sidebar-history imported ${benchmarkHistorySectionOpen.imported ? 'expanded' : 'collapsed'}`} aria-label="가져온 벤치마크">
                                <button
                                    className="benchmark-sidebar-history-toggle"
                                    type="button"
                                    aria-expanded={benchmarkHistorySectionOpen.imported}
                                    aria-controls="benchmark-sidebar-imported-list"
                                    onClick={() => toggleBenchmarkHistorySection('imported')}
                                >
                                    <span aria-hidden="true">{benchmarkHistorySectionOpen.imported ? '⌄' : '›'}</span>
                                    <strong>가져온 벤치마크</strong>
                                    <small>{benchmarkSidebar.imported.length}</small>
                                </button>
                                {benchmarkHistorySectionOpen.imported && <div className="benchmark-sidebar-history-list" id="benchmark-sidebar-imported-list">
                                    {benchmarkSidebar.isHistoryLoading && <small>기록을 불러오는 중…</small>}
                                    {!benchmarkSidebar.isHistoryLoading && benchmarkSidebar.imported.length === 0 && <small>가져온 벤치마크가 없습니다.</small>}
                                    {benchmarkSidebar.imported.map((item) => (
                                        <button
                                            className="benchmark-sidebar-history-item"
                                            key={item.id}
                                            type="button"
                                            disabled={benchmarkBusy}
                                            onClick={() => setBenchmarkOpenRequestID(item.id)}
                                            title={`${item.profileName} · ${item.model} · ${item.profileBaseURL}`}
                                        >
                                            <strong>{item.profileName} · {item.model}</strong>
                                            <span>{item.profileBaseURL}</span>
                                            <small>{item.suiteName} · {item.completedCaseCount}/{item.caseCount}개 · {formatUpdatedAt(item.updatedAt)}</small>
                                        </button>
                                    ))}
                                </div>}
                            </section>
                            <section className={`benchmark-sidebar-history synchronized ${benchmarkHistorySectionOpen.synchronized ? 'expanded' : 'collapsed'}`} aria-label="동기화된 벤치마크">
                                <button
                                    className="benchmark-sidebar-history-toggle"
                                    type="button"
                                    aria-expanded={benchmarkHistorySectionOpen.synchronized}
                                    aria-controls="benchmark-sidebar-synchronized-list"
                                    onClick={() => toggleBenchmarkHistorySection('synchronized')}
                                >
                                    <span aria-hidden="true">{benchmarkHistorySectionOpen.synchronized ? '⌄' : '›'}</span>
                                    <strong>동기화된 벤치마크</strong>
                                    <small>{benchmarkSidebar.synchronized.length}</small>
                                </button>
                                {benchmarkHistorySectionOpen.synchronized && <div className="benchmark-sidebar-history-list" id="benchmark-sidebar-synchronized-list">
                                    {benchmarkSidebar.isHistoryLoading && <small>기록을 불러오는 중…</small>}
                                    {!benchmarkSidebar.isHistoryLoading && benchmarkSidebar.synchronized.length === 0 && <small>동기화된 벤치마크가 없습니다.</small>}
                                    {benchmarkSidebar.synchronized.map((item) => (
                                        <button
                                            className="benchmark-sidebar-history-item"
                                            key={item.id}
                                            type="button"
                                            disabled={benchmarkBusy}
                                            onClick={() => setBenchmarkOpenRequestID(item.id)}
                                            title={`${item.originDeviceName || '다른 PC'} · ${item.profileName} · ${item.model}`}
                                        >
                                            <strong>{item.profileName} · {item.model}</strong>
                                            <span>{item.originDeviceName || '다른 PC'}에서 동기화</span>
                                            <small>{item.suiteName} · {item.completedCaseCount}/{item.caseCount}개 · {formatUpdatedAt(item.updatedAt)}</small>
                                        </button>
                                    ))}
                                </div>}
                            </section>
                        </div>
                        <small>연결 프로필 {savedConnectionProfiles.length}개 · 기본 1개 포함</small>
                    </section>
                ) : (
                    <section className="sync-sidebar" aria-label="결과 동기화">
                        <div className="section-heading"><span>안전한 결과 동기화</span></div>
                        <p>외부 HTTPS와 장치 fingerprint를 사용하는 v2 연결을 준비합니다.</p>
                        <small>API 키, 연결 설정, 대화 내용은 전송하지 않습니다.</small>
                    </section>
                )}
            </aside>
            <button
                className="sidebar-show-button"
                type="button"
                onClick={() => setSidebarVisible(true)}
                aria-label="사이드바 열기"
                title="사이드바 열기"
            >
                <span aria-hidden="true">›</span>
            </button>

            {workspace === 'benchmark' ? (
                <main className="benchmark-panel">
                    <ModelBenchmarkWorkspace
                        profiles={savedConnectionProfiles}
                        connectionAPIKey={apiKey}
                        openRouterModelIDs={openRouterModelIDs}
                        onOpenRouterModelIDsChange={applyOpenRouterModelIDs}
                        onBusyChange={handleBenchmarkBusyChange}
                        onSidebarChange={handleBenchmarkSidebarChange}
                        openBenchmarkID={benchmarkOpenRequestID}
                        onOpenBenchmarkHandled={handleBenchmarkOpenRequestHandled}
                        historyRefreshKey={benchmarkHistoryRefreshKey}
                        onRequestBenchmarkDelete={requestBenchmarkDelete}
                    />
                </main>
            ) : workspace === 'sync' ? (
                <BenchmarkSyncWorkspace />
            ) : connectionSettingsOpen ? (
                renderConnectionSettings()
            ) : (
            <main className="chat-panel" onWheel={handleChatPanelWheel}>
                <header className="chat-header">
                    <div>
                        <span className="eyebrow">CURRENT CONVERSATION</span>
                        {renamingConversation ? (
                            <form className="conversation-title-form" onSubmit={(event) => {
                                event.preventDefault();
                                void saveConversationTitle();
                            }}>
                                <input
                                    value={conversationTitleDraft}
                                    onChange={(event) => setConversationTitleDraft(event.target.value)}
                                    aria-label="대화 이름"
                                    autoFocus
                                />
                                <button className="text-button" type="submit">저장</button>
                                <button className="text-button" type="button" onClick={() => setRenamingConversation(false)}>취소</button>
                            </form>
                        ) : (
                            <strong>{activeConversation?.title || '대화를 선택해 주세요'}</strong>
                        )}
                    </div>
                    <div className="chat-header-actions">
                        {activeConversation && !busy && !renamingConversation && (
                            <button className="text-button" type="button" onClick={beginConversationRename}>
                                이름 변경
                            </button>
                        )}
                        {activeConversation && !busy && (
                            <button className="delete-conversation-button" type="button" onClick={requestConversationDelete}>
                                대화 삭제
                            </button>
                        )}
                    </div>
                </header>

                <div className="message-list" ref={messageListRef} onScroll={handleMessageListScroll}>
                    {!activeConversation ? (
                        <div className="empty-state">
                            <div className="empty-symbol">AI</div>
                            <h1>대화를 선택하거나 새로 시작하세요</h1>
                            <p>왼쪽 목록에서 이전 대화를 열거나 새 대화를 만든 뒤 메시지를 입력하세요.</p>
                            <button className="empty-state-button" type="button" onClick={() => void createConversation()} disabled={busy}>
                                새 대화 시작
                            </button>
                        </div>
                    ) : messages.length === 0 ? (
                        <div className="empty-state">
                            <div className="empty-symbol">AI</div>
                            <h1>로컬 모델과 대화를 시작하세요</h1>
                            <p>왼쪽의 연결 설정에서 서버와 모델을 선택한 뒤 메시지를 입력하세요.</p>
                        </div>
                    ) : (
                        <div className="message-column">
                            {messages.map((message, index) => (
                                <article className={`message ${message.role}`} key={message.id}>
                                    <span className="message-role">{message.role === 'user' ? '나' : 'AI'}</span>
                                    <div className={`message-content${message.role === 'assistant' ? ' markdown-content' : ''}`}>
                                        {message.attachments.length > 0 && (
                                            <div className="message-attachments" aria-label="첨부 파일">
                                                {message.attachments.map((attachment, attachmentIndex) => (
                                                    <span key={`${attachment.name}-${attachmentIndex}`}>
                                                        {attachment.name} · {formatFileSize(attachment.size)}{attachmentDeliveryDescription(attachment)}
                                                    </span>
                                                ))}
                                            </div>
                                        )}
                                        {message.content ? (
                                            message.role === 'assistant' ? (
                                                <AssistantMessageContent content={message.content}/>
                                            ) : message.content
                                        ) : (message.status === 'streaming' ? <span className="typing">생각 중</span> : '')}
                                        {message.status === 'cancelled' && <span className="message-state">중단됨</span>}
                                        {message.status === 'failed' && <span className="message-state error-state">실패</span>}
                                        {message.role === 'assistant' && message.metrics && (
                                            <div className="message-metrics">
                                                {message.model && <span className="message-metrics-model">모델 {message.model}</span>}
                                                <span>응답 {formatDuration(message.metrics.totalDurationMs)}</span>
                                                {message.metrics.firstTokenDurationMs > 0 && (
                                                    <span>첫 토큰 {formatDuration(message.metrics.firstTokenDurationMs)}</span>
                                                )}
                                                {formatGenerationSpeed(message.usage, message.metrics) && (
                                                    <span>{formatGenerationSpeed(message.usage, message.metrics)}</span>
                                                )}
                                            </div>
                                        )}
                                        {message.content && (
                                            <div className="message-actions">
                                                <button className="message-action-button" type="button" onClick={() => void copyMessage(message)}>
                                                    {copiedMessageID === message.id ? '복사됨' : '복사'}
                                                </button>
                                                {message.role === 'assistant' && message.status === 'complete' && (
                                                    <button className="message-action-button" type="button" onClick={() => requestChatShare(message.id)}>
                                                        공유
                                                    </button>
                                                )}
                                                {message.role === 'assistant' && index === messages.length - 1 && !busy && selectedModel && (
                                                    <button className="message-action-button" type="button" onClick={() => void regenerateResponse(message.id)}>
                                                        다시 생성
                                                    </button>
                                                )}
                                            </div>
                                        )}
                                    </div>
                                </article>
                            ))}
                        </div>
                    )}
                </div>

                {canScrollUp && (
                    <button className="scroll-previous-button" type="button" onClick={scrollToPrevious}>
                        이전 메시지 ↑
                    </button>
                )}
                {showScrollToLatest && (
                    <button className="scroll-latest-button" type="button" onClick={scrollToLatest}>
                        최신 메시지 ↓
                    </button>
                )}

                {activeConversation && (
                    <div className="composer-area">
                    {error && <div className="error-banner" role="alert">{error}</div>}
                    <form className="composer" onSubmit={sendMessage}>
                        <div className="composer-reasoning-control">
                            <label htmlFor="chat-reasoning-effort">추론 강도</label>
                            <select
                                id="chat-reasoning-effort"
                                value={reasoningEffort}
                                onChange={(event) => setReasoningEffort(event.target.value as ReasoningEffort)}
                                disabled={busy}
                            >
                                {reasoningEffortOptions.map((option) => (
                                    <option key={option.value || 'auto'} value={option.value}>{option.label}</option>
                                ))}
                            </select>
                            {chatReasoningWarning && (
                                <span className="composer-reasoning-warning" role="status" title={chatReasoningWarning} aria-label={chatReasoningWarning}>
                                    ⚠ 주의
                                </span>
                            )}
                            <div className="composer-token-usage" aria-label="현재 모델의 누적 채팅 토큰 사용량">
                                <span className="composer-token-usage-label">현재 모델 누적</span>
                                {selectedModelUsage ? (
                                    <>
                                        <strong className="composer-token-usage-total">합계 {formatTokenCount(selectedModelUsage.totalTokens)}</strong>
                                        <span className="composer-token-usage-detail">입력 {formatTokenCount(selectedModelUsage.promptTokens)} · 출력 {formatTokenCount(selectedModelUsage.completionTokens)}</span>
                                    </>
                                ) : (
                                    <strong className="composer-token-usage-total">{selectedModel ? '사용량 없음' : '모델 미선택'}</strong>
                                )}
                            </div>
                        </div>
                        <input
                            ref={attachmentInputRef}
                            className="attachment-file-input"
                            type="file"
                            accept={attachmentAccept}
                            multiple
                            onChange={(event) => void addAttachments(event)}
                        />
                        <button
                            className="attachment-add-button"
                            type="button"
                            onClick={() => attachmentInputRef.current?.click()}
                            disabled={!activeConversation || !selectedModel || busy || attachments.length >= maxAttachmentsPerMessage}
                            aria-label="텍스트, 코드, PDF, Word 또는 Excel 문서 첨부"
                            title="텍스트, 코드, PDF, Word 또는 Excel 문서 첨부"
                        >
                            파일
                        </button>
                        <div className="composer-input">
                            {attachments.length > 0 && (
                                <div className="composer-attachments" aria-label="첨부할 파일">
                                    {attachments.map((attachment, index) => (
                                        <span key={`${attachment.name}-${index}`}>
                                            <strong>{attachment.name}</strong>
                                            <small>{formatFileSize(attachment.size)}{textByteSize(attachment.source) > maxAttachmentContentSize ? ' · 전송 시 관련 부분 선택' : ''}</small>
                                            <button
                                                type="button"
                                                onClick={() => removeAttachment(index)}
                                                disabled={busy}
                                                aria-label={`${attachment.name} 첨부 취소`}
                                            >
                                                ×
                                            </button>
                                        </span>
                                    ))}
                                </div>
                            )}
                            <textarea
                                value={input}
                                onChange={(event) => setInput(event.target.value)}
                                onKeyDown={handleComposerKeyDown}
                                placeholder={!activeConversation ? '대화를 선택하거나 새로 시작해 주세요' : selectedModel ? '메시지를 입력하세요' : '먼저 모델을 연결해 주세요'}
                                disabled={!activeConversation || !selectedModel || busy}
                                rows={1}
                            />
                        </div>
                        {busy ? (
                            <button
                                className="stop-button"
                                type="button"
                                onClick={() => void stopGeneration()}
                                aria-label={cancelling ? '생성 중단 중' : '생성 중단'}
                                title={cancelling ? '응답을 중단하는 중입니다' : '생성 중단'}
                                disabled={cancelling}
                            >
                                <span />
                            </button>
                        ) : (
                            <button className="send-button" type="submit" disabled={!canSend} aria-label="전송">↑</button>
                        )}
                    </form>
                    <span className="composer-hint">{cancelling ? '응답을 중단하는 중…' : '텍스트·코드 파일 최대 4개 · 첨부 내용은 연결된 서버로 전송됩니다 · Enter 전송'}</span>
                    </div>
                )}
            </main>
            )}
            {conversationToDelete && (
                <div className="dialog-backdrop" role="presentation">
                    <section className="confirmation-dialog" role="alertdialog" aria-modal="true" aria-labelledby="delete-conversation-title">
                        <h2 id="delete-conversation-title">대화를 삭제할까요?</h2>
                        <p>“{conversationToDelete.title}” 대화와 저장된 기록을 삭제합니다. 이 작업은 되돌릴 수 없습니다.</p>
                        <div className="dialog-actions">
                            <button type="button" className="dialog-cancel-button" onClick={() => setConversationToDelete(null)} disabled={deletingConversation}>
                                취소
                            </button>
                            <button type="button" className="dialog-delete-button" onClick={() => void confirmConversationDelete()} disabled={deletingConversation}>
                                {deletingConversation ? '삭제 중…' : '삭제'}
                            </button>
                        </div>
                    </section>
                </div>
            )}
            {benchmarkToDelete && (
                <div className="dialog-backdrop" role="presentation">
                    <section className="confirmation-dialog" role="alertdialog" aria-modal="true" aria-labelledby="delete-benchmark-title">
                        <h2 id="delete-benchmark-title">벤치마크 기록을 삭제할까요?</h2>
                        <p>“{benchmarkToDelete.model} · {benchmarkToDelete.profileName}” 기록과 저장된 결과를 삭제합니다. 이 작업은 되돌릴 수 없습니다.</p>
                        {benchmarkDeleteError && <p className="dialog-error" role="alert">{benchmarkDeleteError}</p>}
                        <div className="dialog-actions">
                            <button type="button" className="dialog-cancel-button" onClick={() => setBenchmarkToDelete(null)} disabled={deletingBenchmark}>
                                취소
                            </button>
                            <button type="button" className="dialog-delete-button" onClick={() => void confirmBenchmarkDelete()} disabled={deletingBenchmark}>
                                {deletingBenchmark ? '삭제 중…' : '삭제 확인'}
                            </button>
                        </div>
                    </section>
                </div>
            )}
            {connectionProfileToDelete && (
                <div className="dialog-backdrop" role="presentation">
                    <section className="confirmation-dialog" role="alertdialog" aria-modal="true" aria-labelledby="delete-connection-profile-title">
                        <h2 id="delete-connection-profile-title">연결 프로필을 삭제할까요?</h2>
                        <p>“{connectionProfileToDelete.name}” 프로필의 이름과 서버 URL을 삭제합니다. API 키는 저장되지 않았습니다.</p>
                        <div className="dialog-actions">
                            <button type="button" className="dialog-cancel-button" onClick={() => setConnectionProfileToDelete(null)} disabled={deletingConnectionProfile}>
                                취소
                            </button>
                            <button type="button" className="dialog-delete-button" onClick={() => void confirmSavedConnectionProfileDelete()} disabled={deletingConnectionProfile}>
                                {deletingConnectionProfile ? '삭제 중…' : '삭제'}
                            </button>
                        </div>
                    </section>
                </div>
            )}
            {shareTarget && (
                <div className="dialog-backdrop" role="presentation">
                    <section className="share-dialog" role="dialog" aria-modal="true" aria-labelledby="chat-share-title">
                        <h2 id="chat-share-title">응답 공유 저장</h2>
                        <p>저장할 내용과 파일 형식을 선택하세요.</p>
                        <section className="share-content-choice" aria-label="공유할 내용 선택">
                            <span>포함할 내용</span>
                            <div>
                                <button
                                    className={shareContentMode === 'question-answer' ? 'active' : ''}
                                    type="button"
                                    onClick={() => setShareContentMode('question-answer')}
                                    disabled={!shareTarget.question || sharingFormat !== null}
                                >
                                    질문 + 응답
                                </button>
                                <button
                                    className={shareContentMode === 'answer' ? 'active' : ''}
                                    type="button"
                                    onClick={() => setShareContentMode('answer')}
                                    disabled={sharingFormat !== null}
                                >
                                    응답만
                                </button>
                            </div>
                            {shareTarget.question?.attachments.length ? (
                                <small>질문에 첨부된 파일은 이름과 크기만 표시하며 문서 본문은 포함하지 않습니다.</small>
                            ) : (
                                <small>HTML은 읽기 좋고, Markdown은 편집하거나 다른 AI에 전달하기 좋습니다.</small>
                            )}
                        </section>
                        {shareError && <p className="dialog-error" role="alert">{shareError}</p>}
                        <div className="share-dialog-actions">
                            <button className="dialog-cancel-button" type="button" onClick={() => setShareTarget(null)} disabled={sharingFormat !== null}>취소</button>
                            <button className="share-save-button html" type="button" onClick={() => void saveChatShare('html')} disabled={sharingFormat !== null}>
                                {sharingFormat === 'html' ? '저장 중…' : 'HTML로 저장'}
                            </button>
                            <button className="share-save-button markdown" type="button" onClick={() => void saveChatShare('markdown')} disabled={sharingFormat !== null}>
                                {sharingFormat === 'markdown' ? '저장 중…' : 'Markdown으로 저장'}
                            </button>
                        </div>
                    </section>
                </div>
            )}
            <OpenRouterModelPicker
                open={openRouterModelPickerOpen}
                models={models}
                selectedModel={selectedModel}
                selectedModelIDs={openRouterModelIDs}
                onClose={() => setOpenRouterModelPickerOpen(false)}
                onApply={applyOpenRouterModelIDs}
            />
        </div>
    );
}

export default App;
