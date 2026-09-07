import './Loading.css'

interface LoadingProps {
  size?: 'small' | 'medium' | 'large'
}

const Loading: React.FC<LoadingProps> = ({ size = 'medium' }) => {
  return (
    <div className={`loading loading-${size}`}>
      <div className="loading-spinner" />
      <p className="loading-text">Loading...</p>
    </div>
  )
}

export default Loading
