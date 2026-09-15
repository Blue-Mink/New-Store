import React, { useState, useEffect, useCallback } from 'react';
import { fetchSources, addSource, removeSource, type SourceEntry } from '../api/client';
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Badge } from "@/components/ui/badge"
import { Loader2, Plus, Trash2, ExternalLink } from 'lucide-react'
import { toast } from 'sonner'

interface SourceManagerProps {
  /** 源列表变化后通知父组件刷新应用目录 */
  onCatalogChanged?: () => void;
}

const SourceManager: React.FC<SourceManagerProps> = ({ onCatalogChanged }) => {
  const [sources, setSources] = useState<SourceEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [url, setUrl] = useState('');
  const [name, setName] = useState('');
  const [adding, setAdding] = useState(false);
  const [removingId, setRemovingId] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const res = await fetchSources();
      setSources(res.sources || []);
    } catch (e) {
      console.error('Failed to load sources:', e);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const handleAdd = async () => {
    const trimmed = url.trim();
    if (!trimmed) {
      toast.error('请输入应用源地址');
      return;
    }
    setAdding(true);
    try {
      const src = await addSource(trimmed, name.trim() || undefined);
      toast.success(`已添加应用源「${src.name}」`);
      setUrl('');
      setName('');
      await load();
      onCatalogChanged?.();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '添加应用源失败');
    } finally {
      setAdding(false);
    }
  };

  const handleRemove = async (src: SourceEntry) => {
    setRemovingId(src.id);
    try {
      await removeSource(src.id);
      toast.success(`已移除应用源「${src.name}」`);
      await load();
      onCatalogChanged?.();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '删除应用源失败');
    } finally {
      setRemovingId(null);
    }
  };

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-medium">应用源</h3>
        <span className="text-[11px] text-muted-foreground">FnDepot V1/V2</span>
      </div>

      {loading ? (
        <div className="flex justify-center py-3">
          <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
        </div>
      ) : sources.length === 0 ? (
        <p className="text-xs leading-relaxed text-muted-foreground">
          暂无外部应用源。添加后，源中的应用会并入商店目录，来源会在应用上标注。
        </p>
      ) : (
        <div className="space-y-2">
          {sources.map((src) => (
            <div key={src.id} className="flex items-center gap-2 rounded-lg border px-3 py-2">
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="truncate text-sm font-medium">{src.name}</span>
                  {src.app_count > 0 && (
                    <Badge variant="secondary" className="h-5 px-1.5 text-[10px] font-normal">
                      {src.app_count} 个应用
                    </Badge>
                  )}
                  {src.error && (
                    <Badge variant="destructive" className="h-5 px-1.5 text-[10px] font-normal">
                      不可用
                    </Badge>
                  )}
                </div>
                <div className="mt-0.5 flex items-center gap-1.5 text-xs text-muted-foreground">
                  <span className="truncate">{src.url}</span>
                  {src.homepage && (
                    <a
                      href={src.homepage}
                      target="_blank"
                      rel="noreferrer"
                      className="shrink-0 hover:text-foreground"
                      title={src.homepage}
                    >
                      <ExternalLink className="h-3 w-3" />
                    </a>
                  )}
                </div>
                {src.error && (
                  <div className="mt-0.5 truncate text-[11px] text-red-500">{src.error}</div>
                )}
              </div>
              <Button
                variant="ghost"
                size="sm"
                className="h-7 w-7 shrink-0 p-0 text-muted-foreground hover:text-red-500"
                onClick={() => handleRemove(src)}
                disabled={removingId === src.id}
                title="移除应用源"
              >
                {removingId === src.id ? (
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                ) : (
                  <Trash2 className="h-3.5 w-3.5" />
                )}
              </Button>
            </div>
          ))}
        </div>
      )}

      <div className="space-y-2 rounded-lg border bg-muted/30 p-3">
        <Input
          placeholder="源地址：JSON 直链或 GitHub 仓库"
          value={url}
          onChange={(e) => setUrl(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && !adding) handleAdd();
          }}
          className="h-8 text-xs"
        />
        <div className="flex items-center gap-2">
          <Input
            placeholder="显示名（可选）"
            value={name}
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && !adding) handleAdd();
            }}
            className="h-8 flex-1 text-xs"
          />
          <Button size="sm" onClick={handleAdd} disabled={adding || !url.trim()} className="h-8 shrink-0">
            {adding ? (
              <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" />
            ) : (
              <Plus className="mr-1 h-3.5 w-3.5" />
            )}
            {adding ? '验证中…' : '添加'}
          </Button>
        </div>
        <p className="text-[11px] leading-relaxed text-muted-foreground">
          支持 FnDepot 外部应用源协议（V1/V2）：JSON 直链（…/fnpack.json）或 GitHub 仓库根地址。
          添加时即验证可达性与格式。
        </p>
      </div>
    </div>
  );
};

export default SourceManager;
