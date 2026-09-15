import React, { useState, useEffect } from 'react';
import type { AppInfo, AppOperation } from '../api/client';
import { availableVersionLabel, installedVersionLabel, assetUrl } from '../api/client';
import { apiUrl } from '../api/base';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import {
  Package,
  Globe,
  Clock,
  Tag,
  Cpu,
  Network,
  ExternalLink,
  Circle,
  Download,
  RefreshCw,
  BellOff,
  Bell,
  Loader2,
  Trash2,
  User,
  Images,
  Hash,
  HardDrive,
  FileText,
  X,
  ChevronLeft,
  ChevronRight,
} from 'lucide-react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

interface AppDetailDialogProps {
  app: AppInfo | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onInstall: (app: AppInfo) => void;
  onUpdate: (app: AppInfo) => void;
  onIgnoreUpdate?: (app: AppInfo) => void;
  onUnignoreUpdate?: (app: AppInfo) => void;
  onUninstall?: (app: AppInfo) => void;
  operation?: AppOperation;
  /** 点击源名 → 只看该源的应用 */
  onSourceFilter?: (source: string) => void;
  /** 点击作者 → 只看该作者的应用 */
  onAuthorFilter?: (author: string) => void;
}

const DetailRow: React.FC<{ icon: React.ElementType; label: string; children: React.ReactNode }> = ({ icon: Icon, label, children }) => (
  <div className="flex items-start gap-3 py-2">
    <Icon className="h-4 w-4 mt-0.5 text-muted-foreground shrink-0" />
    <div className="flex-1 min-w-0">
      <p className="text-xs text-muted-foreground mb-0.5">{label}</p>
      <div className="text-sm text-foreground break-words">{children}</div>
    </div>
  </div>
);

const formatSize = (bytes?: number): string => {
  if (!bytes || bytes <= 0) return '-';
  if (bytes >= 1024 ** 3) return (bytes / 1024 ** 3).toFixed(2) + ' GB';
  if (bytes >= 1024 ** 2) return (bytes / 1024 ** 2).toFixed(1) + ' MB';
  return (bytes / 1024).toFixed(0) + ' KB';
};

const AppDetailDialog: React.FC<AppDetailDialogProps> = ({ app, open, onOpenChange, onInstall, onUpdate, onIgnoreUpdate, onUnignoreUpdate, onUninstall, operation, onSourceFilter, onAuthorFilter }) => {
  const [readme, setReadme] = useState<string | null>(null);
  const [readmeError, setReadmeError] = useState(false);
  const [lightbox, setLightbox] = useState<number | null>(null);

  // 切换应用时重新拉 README
  useEffect(() => {
    if (!app || !open || !app.has_readme) {
      setReadme(null);
      setReadmeError(false);
      return;
    }
    let cancelled = false;
    setReadme(null);
    setReadmeError(false);
    fetch(assetUrl(app.appname, 'readme'))
      .then((r) => {
        if (!r.ok) throw new Error(r.statusText);
        return r.text();
      })
      .then((text) => { if (!cancelled) setReadme(text); })
      .catch(() => { if (!cancelled) setReadmeError(true); });
    return () => { cancelled = true; };
  }, [app?.appname, open, app?.has_readme]);

  if (!app) return null;

  const isInstalled = app.installed;
  const canUpdate = isInstalled && app.has_update;
  const previewCount = app.preview_count || 0;

  const getStatusColor = (status: string) => {
    switch (status) {
      case 'running': return 'text-emerald-500 fill-emerald-500';
      case 'stopped': return 'text-amber-500 fill-amber-500';
      default: return 'text-muted-foreground/40';
    }
  };

  const getStatusText = (status: string) => {
    switch (status) {
      case 'running': return '运行中';
      case 'stopped': return '已停止';
      default: return status || '未安装';
    }
  };

  const formatDate = (dateStr?: string) => {
    if (!dateStr) return '-';
    try {
      return new Date(dateStr).toLocaleString('zh-CN', {
        year: 'numeric',
        month: '2-digit',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
      });
    } catch {
      return dateStr;
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg max-h-[88vh] overflow-y-auto [&>button.absolute]:top-3 [&>button.absolute]:right-3">
        <DialogHeader>
          <div className="flex items-center gap-3">
            {app.icon_url ? (
              <img
                src={app.icon_url}
                alt={app.display_name}
                className="w-14 h-14 rounded-xl object-cover bg-background dark:bg-muted/60 dark:ring-1 dark:ring-border/50 shrink-0"
              />
            ) : (
              <div className="w-14 h-14 bg-muted/60 rounded-xl flex items-center justify-center text-muted-foreground shrink-0">
                <Package className="h-6 w-6 opacity-40" />
              </div>
            )}
            <div className="flex-1 min-w-0">
              <DialogTitle className="text-base">{app.display_name}</DialogTitle>
              <div className="flex items-center gap-2 mt-1 flex-wrap">
                {isInstalled ? (
                  <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
                    <Circle className={`h-2 w-2 ${getStatusColor(app.status)}`} />
                    <span>{getStatusText(app.status)}</span>
                  </div>
                ) : (
                  <span className="text-xs text-muted-foreground/50">未安装</span>
                )}
                {canUpdate && (
                  <Badge variant="secondary" className="bg-primary/10 text-primary border-0 font-medium px-1.5 h-5 text-[11px] rounded-full">
                    有更新
                  </Badge>
                )}
                {app.update_ignored && (
                  <Badge variant="secondary" className="bg-muted text-muted-foreground border-0 font-medium px-1.5 h-5 text-[11px] rounded-full gap-0.5">
                    <BellOff className="h-2.5 w-2.5" />
                    已忽略更新
                  </Badge>
                )}
              </div>
              {/* 来源 + 作者（点击过滤） */}
              <div className="flex items-center gap-1.5 mt-1.5 flex-wrap">
                {app.source && onSourceFilter && (
                  <button
                    onClick={() => { onOpenChange(false); onSourceFilter(app.source!); }}
                    className="inline-flex items-center gap-0.5 rounded-full bg-primary/10 px-2 h-5 text-[11px] font-medium text-primary hover:bg-primary/20 transition-colors"
                    title={`只看「${app.source}」源的应用`}
                  >
                    <Tag className="h-3 w-3" />
                    {app.source}
                  </button>
                )}
                {app.maintainer && onAuthorFilter && (
                  <button
                    onClick={() => { onOpenChange(false); onAuthorFilter(app.maintainer!); }}
                    className="inline-flex items-center gap-0.5 rounded-full bg-muted px-2 h-5 text-[11px] font-medium text-muted-foreground hover:text-primary hover:bg-primary/10 transition-colors"
                    title={`只看「${app.maintainer}」的应用`}
                  >
                    <User className="h-3 w-3" />
                    {app.maintainer}
                  </button>
                )}
                {app.distributor && app.distributor !== app.maintainer && (
                  <span className="text-[11px] text-muted-foreground/70">
                    发布：{app.distributor}
                    {app.distributor_url && (
                      <a href={app.distributor_url} target="_blank" rel="noreferrer" className="inline-flex ml-0.5 hover:text-primary">
                        <ExternalLink className="h-2.5 w-2.5" />
                      </a>
                    )}
                  </span>
                )}
              </div>
            </div>
          </div>
        </DialogHeader>

        {app.description && (
          <>
            <DialogDescription className="text-sm leading-relaxed">
              {app.description}
            </DialogDescription>
            <Separator />
          </>
        )}

        {/* 预览图画廊 */}
        {previewCount > 0 && (
          <>
            <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <Images className="h-3.5 w-3.5" />
              预览
            </div>
            <div className="flex gap-2 overflow-x-auto pb-1 -mx-1 px-1">
              {Array.from({ length: previewCount }, (_, i) => (
                <button
                  key={i}
                  onClick={() => setLightbox(i)}
                  className="shrink-0 rounded-lg overflow-hidden border border-border/40 hover:opacity-90 transition-opacity"
                  title="点击放大"
                >
                  <img
                    src={assetUrl(app.appname, 'preview', i)}
                    alt={`${app.display_name} 预览 ${i + 1}`}
                    loading="lazy"
                    className="h-28 w-auto object-cover max-w-[220px] bg-muted/40"
                  />
                </button>
              ))}
            </div>
            <Separator />
          </>
        )}

        <div className="space-y-0">
          <DetailRow icon={Tag} label="版本">
            <div className="flex items-center gap-2 flex-wrap">
              <span>{isInstalled ? `v${installedVersionLabel(app)}` : `v${app.latest_version}`}</span>
              {canUpdate && (
                <>
                  <span className="text-muted-foreground">→</span>
                  <span className="text-primary font-medium">v{availableVersionLabel(app)}</span>
                </>
              )}
              {!isInstalled && (
                <span className="text-muted-foreground text-xs">(最新)</span>
              )}
            </div>
          </DetailRow>

          {app.service_port ? (
            <DetailRow icon={Network} label="服务端口">
              {app.service_port}
            </DetailRow>
          ) : null}

          <DetailRow icon={Cpu} label="支持平台">
            {app.platform || '-'}
          </DetailRow>

          {app.size_bytes ? (
            <DetailRow icon={HardDrive} label="安装包大小">
              {formatSize(app.size_bytes)}
            </DetailRow>
          ) : null}

          {app.sha256 && (
            <DetailRow icon={Hash} label="SHA256">
              <code className="text-xs font-mono break-all text-muted-foreground">{app.sha256}</code>
            </DetailRow>
          )}

          <DetailRow icon={Clock} label="最近更新">
            {formatDate(app.updated_at)}
          </DetailRow>

          {app.homepage && (
            <DetailRow icon={Globe} label="官网">
              <a
                href={app.homepage}
                target="_blank"
                rel="noopener noreferrer"
                className="text-primary hover:underline inline-flex items-center gap-1 break-all"
              >
                {app.homepage.replace(/^https?:\/\//, '').replace(/\/$/, '')}
                <ExternalLink className="h-3 w-3 shrink-0" />
              </a>
            </DetailRow>
          )}

          {app.release_url && (
            <DetailRow icon={Tag} label="发布页">
              <a
                href={app.release_url}
                target="_blank"
                rel="noopener noreferrer"
                className="text-primary hover:underline inline-flex items-center gap-1"
              >
                GitHub Release
                <ExternalLink className="h-3 w-3 shrink-0" />
              </a>
            </DetailRow>
          )}
        </div>

        {/* Changelog */}
        {app.changelog && (
          <>
            <Separator />
            <div className="text-xs text-muted-foreground flex items-center gap-1.5">
              <FileText className="h-3.5 w-3.5" />
              更新说明
            </div>
            <p className="text-sm text-foreground/90 whitespace-pre-wrap leading-relaxed">{app.changelog}</p>
          </>
        )}

        {/* README */}
        {app.has_readme && (
          <>
            <Separator />
            <div className="text-xs text-muted-foreground flex items-center gap-1.5">
              <FileText className="h-3.5 w-3.5" />
              README
            </div>
            {readme === null && !readmeError ? (
              <div className="flex items-center gap-2 py-4 text-xs text-muted-foreground">
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
                正在加载 README…
              </div>
            ) : readmeError ? (
              <p className="py-2 text-xs text-muted-foreground">README 加载失败（源服务器不可达）</p>
            ) : (
              <div className="markdown-body text-sm leading-relaxed text-foreground/90 prose prose-sm dark:prose-invert max-w-none
                [&_img]:max-w-full [&_img]:rounded-lg [&_h1]:text-lg [&_h2]:text-base [&_h3]:text-sm [&_h1]:mt-4 [&_h1]:mb-2 [&_h2]:mt-3 [&_h2]:mb-1.5 [&_h3]:mt-2 [&_h3]:mb-1
                [&_pre]:bg-muted [&_pre]:rounded-lg [&_pre]:p-3 [&_pre]:overflow-x-auto [&_code]:text-xs
                [&_table]:w-full [&_table]:text-xs [&_th]:border [&_th]:border-border [&_th]:p-1.5 [&_td]:border [&_td]:border-border [&_td]:p-1.5
                [&_a]:text-primary [&_a]:underline
                [&_ul]:list-disc [&_ul]:pl-5 [&_ol]:list-decimal [&_ol]:pl-5 [&_li]:my-0.5">
                <ReactMarkdown remarkPlugins={[remarkGfm]}>
                  {readme || ''}
                </ReactMarkdown>
              </div>
            )}
          </>
        )}

        <Separator />

        <div className="flex justify-end gap-2">
          <Button
            size="sm"
            variant="ghost"
            asChild
            className="rounded-full px-4"
          >
            <a href={apiUrl(`/api/apps/${app.appname}/download`)} download>
              <Download className="mr-1.5 h-3.5 w-3.5" />
              下载 fpk
            </a>
          </Button>
          {isInstalled && onUninstall && (
            <Button
              size="sm"
              variant="ghost"
              onClick={() => { onOpenChange(false); onUninstall(app); }}
              disabled={!!operation}
              aria-label={`卸载 ${app.display_name}`}
              className="rounded-full px-4 text-muted-foreground hover:text-destructive hover:bg-destructive/10"
            >
              <Trash2 className="mr-1.5 h-3.5 w-3.5" />
              卸载
            </Button>
          )}
          {app.update_ignored && onUnignoreUpdate && (
            <Button
              size="sm"
              variant="ghost"
              onClick={() => onUnignoreUpdate(app)}
              className="rounded-full px-4 text-muted-foreground"
            >
              <Bell className="mr-1.5 h-3.5 w-3.5" />
              取消忽略
            </Button>
          )}
          {operation ? (
            <Button size="sm" disabled className="rounded-full px-4">
              <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
              {operation.message || '处理中...'}
            </Button>
          ) : !isInstalled ? (
            <Button
              size="sm"
              onClick={() => { onOpenChange(false); onInstall(app); }}
              className="rounded-full px-4"
            >
              <Download className="mr-1.5 h-3.5 w-3.5" />
              安装
            </Button>
          ) : canUpdate ? (
            <>
              {onIgnoreUpdate && (
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => onIgnoreUpdate(app)}
                  className="rounded-full px-4 text-muted-foreground"
                >
                  <BellOff className="mr-1.5 h-3.5 w-3.5" />
                  忽略更新
                </Button>
              )}
              <Button
                size="sm"
                variant="outline"
                onClick={() => { onOpenChange(false); onUpdate(app); }}
                className="rounded-full px-4 border-primary text-primary hover:bg-primary/10"
              >
                <RefreshCw className="mr-1.5 h-3.5 w-3.5" />
                更新
              </Button>
            </>
          ) : null}
        </div>
      </DialogContent>

      {/* 预览图灯箱 */}
      {lightbox != null && (
        <div
          className="fixed inset-0 z-[60] bg-black/90 flex items-center justify-center p-6"
          onClick={() => setLightbox(null)}
        >
          <button className="absolute top-4 right-4 text-white/80 hover:text-white" onClick={() => setLightbox(null)} aria-label="关闭">
            <X className="h-6 w-6" />
          </button>
          <button
            className="absolute left-4 top-1/2 -translate-y-1/2 text-white/60 hover:text-white disabled:opacity-0"
            disabled={lightbox === 0}
            onClick={(e) => { e.stopPropagation(); setLightbox((lightbox - 1 + previewCount) % previewCount); }}
            aria-label="上一张"
          >
            <ChevronLeft className="h-8 w-8" />
          </button>
          <img
            src={assetUrl(app.appname, 'preview', lightbox)}
            alt={`${app.display_name} 预览 ${lightbox + 1}`}
            className="max-w-full max-h-full object-contain rounded-lg"
            onClick={(e) => e.stopPropagation()}
          />
          <button
            className="absolute right-4 top-1/2 -translate-y-1/2 text-white/60 hover:text-white disabled:opacity-0"
            disabled={lightbox === previewCount - 1}
            onClick={(e) => { e.stopPropagation(); setLightbox((lightbox + 1) % previewCount); }}
            aria-label="下一张"
          >
            <ChevronRight className="h-8 w-8" />
          </button>
        </div>
      )}
    </Dialog>
  );
};

export default AppDetailDialog;
