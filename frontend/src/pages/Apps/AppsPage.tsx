import React, { useState } from "react";
import { Button, Empty, Input, Spin } from "antd";
import { ArrowRightOutlined, SearchOutlined } from "@ant-design/icons";
import { useNavigate } from "react-router-dom";
import { useCatalogUiStore } from "@/stores/useCatalogUiStore";
import { useApplicationPage } from "@/hooks/useApplicationPage";
import type { V2Application } from "@/services/runApi";
import { useIsMobile } from "@/shell/useIsMobile";
import MobileAppCenter from "./MobileAppCenter";
import "./AppsPage.css";

const { Search } = Input;
const KIND_LABELS: Record<string, string> = {
  page: "页面",
  form: "表单",
  dashboard: "看板",
  custom: "应用",
  task: "任务",
};
const cardDelay = (index: number): React.CSSProperties => ({
  animationDelay: `${Math.min(index, 8) * 60}ms`,
});

/** Desktop grid with thumbnails — unchanged (开发执行报告 §20/§54). */
const DesktopAppCenter: React.FC = () => {
  const navigate = useNavigate();
  const category = useCatalogUiStore((state) => state.fixedCategory);
  const [query, setQuery] = useState("");
  const { items, loading, loadingMore, hasMore, loadMore } = useApplicationPage(
    {
      kind: "fixed",
      scope: "accessible",
      mode: "consume",
      category,
      query,
      limit: 24,
    },
  );
  const open = (app: V2Application) =>
    navigate(`/app/${encodeURIComponent(app.slug)}`);
  return (
    <div className="apps-page animate-fade-in">
      <div className="page-header">
        <h1 className="page-title">应用中心</h1>
        <p className="page-subtitle">发现并打开企业已授权的固定应用</p>
      </div>
      <div className="page-toolbar">
        <Search
          placeholder="搜索应用..."
          prefix={<SearchOutlined />}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          allowClear
          className="max-w-xs"
        />
      </div>
      {loading && items.length === 0 ? (
        <div className="flex items-center justify-center py-20">
          <Spin size="large" />
        </div>
      ) : items.length === 0 ? (
        <div className="flex items-center justify-center py-20">
          <Empty description="暂无可用应用" />
        </div>
      ) : (
        <>
          <div className="app-grid">
            {items.map((app, index) => (
              <div
                key={app.id}
                className="app-card"
                style={cardDelay(index)}
                role="button"
                tabIndex={0}
                onClick={() => open(app)}
                onKeyDown={(event) => {
                  if (event.key === "Enter" || event.key === " ") open(app);
                }}
              >
                <div
                  className="app-card-thumb"
                  style={
                    app.color
                      ? {
                          background: `linear-gradient(135deg, ${app.color}, color-mix(in srgb, ${app.color} 40%, #000))`,
                        }
                      : undefined
                  }
                >
                  <span className="app-card-emoji">{app.icon || "🧩"}</span>
                </div>
                <div className="app-card-body">
                  <div className="app-card-name">{app.name}</div>
                  <div className="app-card-desc">{app.description}</div>
                  <div className="app-card-footer">
                    <div className="app-card-tags">
                      {app.category_name && (
                        <span className="app-card-cat">
                          {app.category_name}
                        </span>
                      )}
                      <span className="app-card-tag">
                        {KIND_LABELS[app.kind] || app.kind}
                      </span>
                    </div>
                    <span className="app-card-open">
                      打开 <ArrowRightOutlined />
                    </span>
                  </div>
                </div>
              </div>
            ))}
          </div>
          <div className="apps-page__more">
            {hasMore ? (
              <Button loading={loadingMore} onClick={() => void loadMore()}>
                加载更多应用
              </Button>
            ) : (
              items.length > 24 && (
                <span className="apps-page__count">
                  已显示全部 {items.length} 个
                </span>
              )
            )}
          </div>
        </>
      )}
    </div>
  );
};

/** Router adapter (开发执行报告 §18): React-level switch, no CSS hiding. */
const AppsPage: React.FC = () => {
  const isMobile = useIsMobile();
  return isMobile ? <MobileAppCenter /> : <DesktopAppCenter />;
};
export default AppsPage;
