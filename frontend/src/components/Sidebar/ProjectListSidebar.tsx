import React, { useEffect } from 'react';
import { List, Button, Empty, Popconfirm } from 'antd';
import { PlusOutlined, DeleteOutlined, FolderOpenOutlined } from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import { useProjectStore } from '@/stores/useProjectStore';

const ProjectListSidebar: React.FC = () => {
  const navigate = useNavigate();
  const { projects, loadProjects, createProject, deleteProject } = useProjectStore();

  useEffect(() => {
    loadProjects();
  }, [loadProjects]);

  const handleNewProject = async () => {
    const title = prompt('项目名称:');
    if (!title) return;
    try {
      const project = await createProject({ title, description: '' });
      navigate(`/workspace/${project.id}`);
    } catch (error) {
      console.error('Failed to create project:', error);
    }
  };

  return (
    <div style={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
      <div style={{ padding: 16, borderBottom: '1px solid #f0f0f0' }}>
        <Button type="primary" icon={<PlusOutlined />} onClick={handleNewProject} block>
          新建项目
        </Button>
      </div>
      <div style={{ flex: 1, overflow: 'auto', padding: '0 8px' }}>
        {projects.length === 0 ? (
          <Empty description="暂无项目" style={{ marginTop: 40 }} />
        ) : (
          <List
            dataSource={projects}
            renderItem={(item) => (
              <List.Item
                key={item.id}
                style={{ cursor: 'pointer' }}
                onClick={() => navigate(`/workspace/${item.id}`)}
              >
                <FolderOpenOutlined style={{ marginRight: 8 }} />
                <div style={{ flex: 1 }}>
                  <div style={{ fontWeight: 500 }}>{item.title}</div>
                  <div style={{ fontSize: 12, color: '#999' }}>
                    {new Date(item.updated_at).toLocaleDateString()}
                  </div>
                </div>
                <Popconfirm
                  title="确定删除此项目？"
                  onConfirm={(e) => {
                    e?.stopPropagation();
                    deleteProject(item.id);
                  }}
                  onCancel={(e) => e?.stopPropagation()}
                >
                  <DeleteOutlined
                    style={{ color: '#999' }}
                    onClick={(e) => e.stopPropagation()}
                  />
                </Popconfirm>
              </List.Item>
            )}
          />
        )}
      </div>
    </div>
  );
};

export default ProjectListSidebar;
