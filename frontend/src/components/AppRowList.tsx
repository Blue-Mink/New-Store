import React from 'react';
import type { AppInfo, AppOperation } from '../api/client';
import { availableVersionLabel, installedVersionLabel } from '../api/client';
import { cn, formatCount, formatSpeed, formatProgress } from '@/lib/utils';
import { Progress } from '@/components/ui/progress';
import { Badge } from '@/components/ui/badge';
import {
  Download, Package, Circle, Container, X, BellOff, Trash2,
} from 'lucide-react';
import { CheckCircle2, RefreshCw as UpdateIcon, Search } from 'lucide-react';

interface AppRowListProps {
  apps: AppInfo[];
  onInstall: (app: AppInfo) => void;
  onUpdate: (app: AppInfo) => void;
  onUninstall: (app: AppInfo) => void;
  onDetail: (app: AppInfo) => void;
  onCancelOp?: (app: AppInfo) => void;
  appOperations?: Map<string, AppOperation>;
  searchQuery?: string;
  filterType?: string;
  /** false when this fnOS build cannot update apps without destroying them. */
  upgradeAllowed?: boolean;
}

const STATUS_TEXT: Record<string, string> = {
  running: '运行中',
  stopped: '已停止',
  installing: '安装中',
  uninstalling: '卸载中',
  updating: '更新中',
};

const statusColor = (s: string) =>
  s === 'running' ? 'text-emerald-500' : s === 'stopped' ? 'text-amber-500' : 'text-primary';

/**
 * 移动端 App Store 风格列表：
 * iOS grouped table（白色圆角容器 + 发丝线分隔），每行 =
 * 超椭圆图标 + 名称/开发者/描述/版本 + 右侧药丸按钮，
 * 关键信息一屏可见，无需二次点击。
 * 注意：行内应用名用 <span>（非 heading），避免与桌面卡片
 * 的 e2e heading 选择器冲突（桌面布局下本组件 display:none）。
 */
const AppRowList: React.FC<AppRowListProps> = ({
  apps, onInstall, onUpdate, onUninstall, onDetail, onCancelOp, appOperations, searchQuery, filterType, upgradeAllowed = true,
}) => {
  if (apps.length === 0) {
    const emptyText = searchQuery?.trim()
      ? `未找到匹配「${searchQuery.trim()}」的应用`
      : filterType === 'installed' ? '暂无已安装的应用'
      : filterType === 'update_available' ? '所有应用都是最新版本'
      : '暂无可用应用';
    const Icon = searchQuery?.trim() ? Search : filterType === 'installed' ? CheckCircle2 : filterType === 'update_available' ? UpdateIcon : Search;
    return (
      <div className="flex flex-col items-center justify-center h-64 text-muted-foreground">
        <Icon className="h-12 w-12 mb-4 opacity-40" />
        <p className="text-sm">{emptyText}</p>
      </div>
    );
  }

  return (
    <div className="bg-card rounded-[18px] overflow-hidden border border-border/20 shadow-appstore">
      {apps.map((app, i) => {
        const operation = appOperations?.get(app.appname);
        const isInstalled = app.installed;
        const canUpdate = isInstalled && app.has_update;
        return (
          <div
            key={app.appname}
            className={cn(
              "flex items-center gap-3.5 p-4 cursor-pointer transition-colors hover:bg-muted/30 active:bg-muted/50",
              i > 0 && "border-t border-border/40"
            )}
            onClick={() => onDetail(app)}
          >
            {/* 图标 */}
            {app.icon_url ? (
              <img
                src={app.icon_url}
                alt=""
                className="w-14 h-14 squircle object-cover bg-muted/40 shrink-0"
                loading="lazy"
              />
            ) : (
              <div className="w-14 h-14 bg-muted/60 squircle flex items-center justify-center text-muted-foreground shrink-0">
                <Package className="h-6 w-6 opacity-40" />
              </div>
            )}

            {/* 中部信息 */}
            <div className="flex-1 min-w-0">
              <div className="flex items-center gap-1.5 min-w-0">
                <span className="font-semibold text-[16px] leading-tight truncate" title={app.display_name}>
                  {app.display_name}
                </span>
                {app.app_type === 'docker' && (
                  <Container className="h-3.5 w-3.5 text-primary shrink-0" />
                )}
                {canUpdate && (
                  <Badge variant="secondary" className="bg-primary/10 text-primary border-0 font-medium px-1.5 h-5 text-[11px] shrink-0 rounded-full">
                    有更新
                  </Badge>
                )}
                {app.update_ignored && (
                  <Badge variant="secondary" className="bg-muted text-muted-foreground border-0 font-medium px-1.5 h-5 text-[11px] shrink-0 rounded-full gap-0.5">
                    <BellOff className="h-2.5 w-2.5" />已忽略
                  </Badge>
                )}
              </div>

              <p className="text-[13px] text-muted-foreground/80 truncate" title={app.appname}>
                {app.appname}
              </p>

              {app.description && (
                <p className="text-[13px] text-muted-foreground/80 leading-snug line-clamp-2 mt-0.5">
                  {app.description}
                </p>
              )}

              <div className="flex items-center flex-wrap gap-x-1.5 text-xs text-muted-foreground mt-1">
                <span>v{isInstalled ? installedVersionLabel(app) : app.latest_version}</span>
                {canUpdate && (
                  <span className="text-primary">→ v{availableVersionLabel(app)}</span>
                )}
                {app.download_count != null && app.download_count > 0 && (
                  <>
                    <span className="text-muted-foreground/30">·</span>
                    <span className="inline-flex items-center gap-0.5">
                      <Download className="h-3 w-3" />{formatCount(app.download_count)}
                    </span>
                  </>
                )}
                {isInstalled && (
                  <>
                    <span className="text-muted-foreground/30">·</span>
                    <span className={cn("inline-flex items-center gap-1", statusColor(app.status))}>
                      <Circle className="h-1.5 w-1.5 fill-current" />
                      {STATUS_TEXT[app.status] || app.status}
                    </span>
                  </>
                )}
              </div>

              {/* 进行中的操作：紧凑进度条 */}
              {operation && (
                <div className="mt-2 space-y-1.5">
                  <Progress value={operation.progress} className="h-1.5" />
                  <div className="flex items-center justify-between text-xs text-muted-foreground">
                    <span className="min-w-0 truncate">
                      {operation.message}
                      {operation.step === 'downloading' && operation.speed != null && operation.speed > 0 && ` · ${formatSpeed(operation.speed)}`}
                      {operation.downloaded != null && operation.total != null && operation.total > 0 && ` · ${formatProgress(operation.downloaded, operation.total)}`}
                    </span>
                    {(operation.step === 'downloading' || operation.step === 'pulling') && operation.cancel && (
                      <button
                        onClick={(e) => { e.stopPropagation(); onCancelOp?.(app); }}
                        className="shrink-0 p-1 -m-1 rounded-full text-muted-foreground hover:text-destructive hover:bg-destructive/10"
                        aria-label="取消"
                      >
                        <X className="h-3.5 w-3.5" />
                      </button>
                    )}
                  </div>
                </div>
              )}
            </div>

            {/* 右侧操作（App Store GET / UPDATE 药丸） */}
            {!operation && (
              <div className="shrink-0 flex flex-col items-end gap-1.5" onClick={e => e.stopPropagation()}>
                {!isInstalled ? (
                  <button
                    onClick={() => onInstall(app)}
                    className="pill bg-primary text-primary-foreground h-7 px-4 text-[13px] font-semibold shadow-sm active:opacity-80"
                  >
                    获取
                  </button>
                ) : canUpdate ? (
                  <button
                    onClick={() => onUpdate(app)}
                    disabled={!upgradeAllowed}
                    title={upgradeAllowed ? undefined : '当前 fnOS 版本的更新通道会删除应用数据，请在系统应用中心手动安装 fpk'}
                    className="pill h-7 px-3.5 text-[13px] font-semibold border border-primary/50 text-primary active:bg-primary/10 disabled:border-muted disabled:text-muted-foreground"
                  >
                    {upgradeAllowed ? '更新' : '需手动'}
                  </button>
                ) : null}
                {isInstalled && onUninstall && (
                  <button
                    onClick={() => onUninstall(app)}
                    aria-label={`卸载 ${app.display_name}`}
                    title="卸载"
                    className="p-1.5 rounded-full text-muted-foreground/60 hover:text-destructive hover:bg-destructive/10"
                  >
                    <Trash2 className="h-4 w-4" />
                  </button>
                )}
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
};

export default AppRowList;
