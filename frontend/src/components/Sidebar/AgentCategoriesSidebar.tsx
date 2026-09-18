import React, { useEffect } from "react";
import { Menu } from "antd";
import { useCatalogUiStore } from "@/stores/useCatalogUiStore";
import { useWorkspaceBootstrapStore } from "@/stores/useWorkspaceBootstrapStore";

const AgentCategoriesSidebar: React.FC = () => {
  const categories = useWorkspaceBootstrapStore(
    (state) => state.agentCategories,
  );
  const load = useWorkspaceBootstrapStore((state) => state.load);
  const selected = useCatalogUiStore((state) => state.agentCategory);
  const setSelected = useCatalogUiStore((state) => state.setAgentCategory);
  useEffect(() => {
    void load();
  }, [load]);
  return (
    <Menu
      mode="inline"
      selectedKeys={[selected || "all"]}
      items={[
        { key: "all", label: "全部智能体" },
        ...categories.map((cat) => ({
          key: cat.slug,
          label: `${cat.name} (${cat.count})`,
        })),
      ]}
      onClick={({ key }) => setSelected(key === "all" ? null : key)}
    />
  );
};
export default AgentCategoriesSidebar;
