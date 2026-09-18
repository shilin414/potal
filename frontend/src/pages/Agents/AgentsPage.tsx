import React, { useState } from "react";
import { Button, Empty, Input, Spin, Tag, message } from "antd";
import { SearchOutlined, StarFilled, StarOutlined } from "@ant-design/icons";
import { useNavigate } from "react-router-dom";
import AgentAvatar from "@/components/Agents/AgentAvatar";
import { useApplicationPage } from "@/hooks/useApplicationPage";
import { useCatalogUiStore } from "@/stores/useCatalogUiStore";
import { applicationConsumeBlock } from "@/lib/applicationConsumability";
import { setApplicationFavorite, type V2Application } from "@/services/runApi";
import { useIsMobile } from "@/shell/useIsMobile";
import MobileAgentCenter from "./MobileAgentCenter";
import "./AgentsPage.css";

const { Search } = Input;
const PAGE_SIZE = 24;
const cardDelay = (index: number): React.CSSProperties => ({
  animationDelay: `${Math.min(index, 8) * 40}ms`,
});

/** Desktop grid — unchanged (开发执行报告 §54: Desktop 不允许回归). */
const DesktopAgentCenter: React.FC = () => {
  const navigate = useNavigate();
  const category = useCatalogUiStore((state) => state.agentCategory);
  const [query, setQuery] = useState("");
  const { items, loading, loadingMore, hasMore, loadMore, patchItem } =
    useApplicationPage({
      kind: "chat",
      scope: "accessible",
      mode: "consume",
      category,
      query,
      limit: PAGE_SIZE,
    });

  const toggleFavorite = async (agent: V2Application) => {
    const next = !agent.is_favorite;
    try {
      await setApplicationFavorite(agent.id, next);
      patchItem(agent.id, { is_favorite: next });
    } catch {
      message.error("收藏操作失败");
    }
  };

  const open = (agent: V2Application) => {
    const block = applicationConsumeBlock(agent);
    if (block) {
      message.warning(block);
      return;
    }
    navigate(`/chat/${encodeURIComponent(agent.slug)}`);
  };

  return (
    <div className="agents-page animate-fade-in">
      <div className="page-header">
        <h1 className="page-title">智能体中心</h1>
        <p className="page-subtitle">发现、收藏并使用企业已授权的智能体</p>
      </div>
      <div className="page-toolbar">
        <Search
          placeholder="搜索智能体..."
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
          <Empty description="暂无可用智能体" />
        </div>
      ) : (
        <>
          <div className="agent-grid">
            {items.map((agent, index) => (
              <div
                key={agent.id}
                className="agent-card"
                style={cardDelay(index)}
                role="button"
                tabIndex={0}
                onClick={() => open(agent)}
                onKeyDown={(event) => {
                  if (event.key === "Enter" || event.key === " ") open(agent);
                }}
              >
                <div className="agent-card-top">
                  <AgentAvatar application={agent} size={54} />
                  <div className="agent-card-title-wrap">
                    <h3 className="agent-card-name">{agent.name}</h3>
                    <span className="agent-card-provider">
                      {agent.provider_key || "企业智能体"}
                    </span>
                  </div>
                  <Button
                    type="text"
                    aria-label={agent.is_favorite ? "取消收藏" : "收藏"}
                    icon={
                      agent.is_favorite ? (
                        <StarFilled style={{ color: "#f59e0b" }} />
                      ) : (
                        <StarOutlined />
                      )
                    }
                    onClick={(event) => {
                      event.stopPropagation();
                      void toggleFavorite(agent);
                    }}
                  />
                </div>
                <p className="agent-card-desc">
                  {agent.description || "暂无描述"}
                </p>
                <div className="agent-card-footer">
                  <span className="agent-card-tags">
                    {agent.category_name && (
                      <Tag className="agent-tag">{agent.category_name}</Tag>
                    )}
                    {agent.is_default_agent && <Tag color="gold">默认</Tag>}
                  </span>
                  <span className="agent-card-meta">打开对话</span>
                </div>
              </div>
            ))}
          </div>
          <div className="agents-page__more">
            {hasMore ? (
              <Button loading={loadingMore} onClick={() => void loadMore()}>
                加载更多智能体
              </Button>
            ) : (
              items.length > PAGE_SIZE && (
                <span className="agents-page__count">
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

/**
 * Router adapter (开发执行报告 §11): React-level switch, never CSS hiding —
 * the two centres are different information architectures, not two sizes of
 * the same grid.
 */
const AgentsPage: React.FC = () => {
  const isMobile = useIsMobile();
  return isMobile ? <MobileAgentCenter /> : <DesktopAgentCenter />;
};
export default AgentsPage;
