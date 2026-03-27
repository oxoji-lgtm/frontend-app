export type User = {
  id: string;
  name: string;
  email: string;
  role: string;
}

export type Post = {
  id: string;
  title: string;
  content: string;
  userId: string;
}

export type Comment = {
  id: string;
  content: string;
  postId: string;
  userId: string;
}

export type LoginCredentials = {
  email: string;
  password: string;
}

export type RegisterUser = {
  name: string;
  email: string;
  password: string;
}

export type LoginResponse = {
  token: string;
  user: User;
}

export type RegisterResponse = {
  user: User;
}